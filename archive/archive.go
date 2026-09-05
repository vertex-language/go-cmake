// Package archive creates and extracts the archive formats CMake handles.
//
// It exists because three separate things need it and none of them should grow
// its own copy: `cmake -E tar`, `file(ARCHIVE_CREATE)` and `file(ARCHIVE_EXTRACT)`,
// and FetchContent unpacking a release tarball.
//
// Extraction is the dangerous direction. An archive is untrusted input -- it
// came off the network, or from a project this build did not write -- and an
// entry named "../../.ssh/authorized_keys" would escape the directory it was
// asked to unpack into. Every path is therefore checked against the destination
// rather than trusted, and an entry that escapes is refused rather than
// sanitised, because a silently renamed file is a different archive from the
// one the caller asked for.
package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Format is an archive encoding.
type Format string

const (
	Tar     Format = "tar"
	TarGz   Format = "tar.gz"
	Zip     Format = "zip"
	Unknown Format = ""
)

// DetectFormat identifies an archive from its name.
func DetectFormat(name string) Format {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return TarGz
	case strings.HasSuffix(lower, ".tar"):
		return Tar
	case strings.HasSuffix(lower, ".zip"):
		return Zip
	default:
		return Unknown
	}
}

// maxEntries bounds how many members an archive may have. An archive with
// millions of tiny entries is a denial of service rather than a package, and a
// build tool that unpacks whatever it is handed is a poor place to find that out.
const maxEntries = 1 << 20

// Extract unpacks an archive into a directory.
//
// stripComponents removes leading path components from every entry, which is
// what makes a release tarball whose contents sit under "project-1.2.3/" unpack
// into the destination directory rather than one below it.
func Extract(archivePath, dest string, stripComponents int) error {
	format := DetectFormat(archivePath)
	if format == Unknown {
		return fmt.Errorf("archive: cannot tell the format of %s from its name", archivePath)
	}
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return err
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	switch format {
	case Zip:
		return extractZip(archivePath, absDest, stripComponents)
	case TarGz:
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("archive: %s is not gzip: %w", archivePath, err)
		}
		defer gz.Close()
		return extractTar(gz, absDest, stripComponents)
	default:
		return extractTar(f, absDest, stripComponents)
	}
}

func extractTar(r io.Reader, dest string, strip int) error {
	tr := tar.NewReader(r)
	for n := 0; ; n++ {
		if n > maxEntries {
			return fmt.Errorf("archive: more than %d entries", maxEntries)
		}
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, ok := safeJoin(dest, header.Name, strip)
		if !ok {
			continue // stripped away entirely
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeFile(target, tr, os.FileMode(header.Mode).Perm()|0600); err != nil {
				return err
			}
		case tar.TypeSymlink, tar.TypeLink:
			// A link is skipped rather than created. Its target is a path the
			// archive chose, and following one out of the destination is the
			// same escape the path check exists to prevent.
			continue
		}
	}
}

func extractZip(archivePath, dest string, strip int) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()
	if len(zr.File) > maxEntries {
		return fmt.Errorf("archive: more than %d entries", maxEntries)
	}
	for _, entry := range zr.File {
		target, ok := safeJoin(dest, entry.Name, strip)
		if !ok {
			continue
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return err
		}
		err = writeFile(target, rc, entry.Mode().Perm()|0600)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// safeJoin resolves an archive entry's path against the destination, refusing
// anything that would land outside it.
//
// The check is on the resolved path rather than on the name, because ".." is
// not the only way out: an absolute name, a Windows drive letter, and a name
// that is harmless until a component is stripped all reach the same place.
func safeJoin(dest, name string, strip int) (string, bool) {
	name = strings.ReplaceAll(name, "\\", "/")
	parts := strings.Split(path.Clean("/"+name), "/")
	// path.Clean("/"+name) makes the name absolute and removes any leading
	// "..", so the first element is always empty.
	parts = parts[1:]
	if strip > 0 {
		if len(parts) <= strip {
			return "", false
		}
		parts = parts[strip:]
	}
	if len(parts) == 0 || parts[0] == "" {
		return "", false
	}
	// A Windows drive letter survives path.Clean, because it is an ordinary
	// component to a slash-path. Joining it produces "dest\C:Users", which is
	// not a path at all -- so the entry is refused here rather than failing
	// later with an error about a directory name being invalid.
	if isDriveSpec(parts[0]) {
		return "", false
	}
	target := filepath.Join(append([]string{dest}, parts...)...)
	if !withinDir(dest, target) {
		return "", false
	}
	return target, true
}

// isDriveSpec reports whether a path component is a Windows drive letter.
func isDriveSpec(part string) bool {
	if len(part) != 2 || part[1] != ':' {
		return false
	}
	c := part[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// withinDir reports whether target is dest or inside it.
func withinDir(dest, target string) bool {
	rel, err := filepath.Rel(dest, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func writeFile(target string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

// Create writes an archive of the given paths.
//
// Paths are stored relative to baseDir, which is what makes an archive unpack
// into a directory of its own rather than scattering absolute paths.
func Create(archivePath string, format Format, baseDir string, paths []string) error {
	if format == Unknown {
		format = DetectFormat(archivePath)
	}
	if format == Unknown {
		return fmt.Errorf("archive: cannot tell the format of %s from its name", archivePath)
	}
	if err := os.MkdirAll(filepath.Dir(archivePath), 0755); err != nil {
		return err
	}
	out, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	defer out.Close()

	members, err := collect(baseDir, paths)
	if err != nil {
		return err
	}

	switch format {
	case Zip:
		return writeZip(out, baseDir, members)
	case TarGz:
		gz := gzip.NewWriter(out)
		defer gz.Close()
		return writeTar(gz, baseDir, members)
	default:
		return writeTar(out, baseDir, members)
	}
}

// collect expands the requested paths into the files to store, relative to
// baseDir and sorted so that the same input produces the same archive.
func collect(baseDir string, paths []string) ([]string, error) {
	seen := map[string]bool{}
	var members []string
	for _, p := range paths {
		full := p
		if !filepath.IsAbs(full) {
			full = filepath.Join(baseDir, p)
		}
		err := filepath.Walk(full, func(name string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(baseDir, name)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if rel == "." || seen[rel] {
				return nil
			}
			seen[rel] = true
			members = append(members, rel)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(members)
	return members, nil
}

func writeTar(w io.Writer, baseDir string, members []string) error {
	tw := tar.NewWriter(w)
	defer tw.Close()
	for _, rel := range members {
		full := filepath.Join(baseDir, filepath.FromSlash(rel))
		info, err := os.Lstat(full)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel
		if info.IsDir() {
			header.Name += "/"
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			if err := copyInto(tw, full); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeZip(w io.Writer, baseDir string, members []string) error {
	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, rel := range members {
		full := filepath.Join(baseDir, filepath.FromSlash(rel))
		info, err := os.Lstat(full)
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = rel
		header.Method = zip.Deflate
		if info.IsDir() {
			header.Name += "/"
		}
		entry, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			if err := copyInto(entry, full); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyInto(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// List names an archive's members without extracting anything, which is what
// `cmake -E tar t` reports.
func List(archivePath string) ([]string, error) {
	format := DetectFormat(archivePath)
	if format == Unknown {
		return nil, fmt.Errorf("archive: cannot tell the format of %s from its name", archivePath)
	}
	if format == Zip {
		zr, err := zip.OpenReader(archivePath)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		out := make([]string, 0, len(zr.File))
		for _, entry := range zr.File {
			out = append(out, entry.Name)
		}
		return out, nil
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r io.Reader = f
	if format == TarGz {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r = gz
	}
	var out []string
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, header.Name)
	}
}
