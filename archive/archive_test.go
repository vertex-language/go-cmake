package archive_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vertex-language/go-cmake/archive"
)

// The round trip is the easy half. The half worth testing is extraction of an
// archive nobody here wrote: it arrives off the network, and an entry that
// escapes the destination is the difference between unpacking a dependency and
// overwriting something outside the build tree.

func TestRoundTrip(t *testing.T) {
	for _, name := range []string{"out.tar", "out.tar.gz", "out.zip"} {
		t.Run(name, func(t *testing.T) {
			src := t.TempDir()
			mustWrite(t, filepath.Join(src, "a.txt"), "one")
			mustWrite(t, filepath.Join(src, "sub", "b.txt"), "two")

			archivePath := filepath.Join(t.TempDir(), name)
			if err := archive.Create(archivePath, archive.Unknown, src, []string{"."}); err != nil {
				t.Fatalf("create: %v", err)
			}

			dest := t.TempDir()
			if err := archive.Extract(archivePath, dest, 0); err != nil {
				t.Fatalf("extract: %v", err)
			}
			assertFile(t, filepath.Join(dest, "a.txt"), "one")
			assertFile(t, filepath.Join(dest, "sub", "b.txt"), "two")
		})
	}
}

// TestStripComponents covers what a release tarball needs: its contents sit
// under a versioned directory that the caller does not want.
func TestStripComponents(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "project-1.2.3", "README"), "hello")
	mustWrite(t, filepath.Join(src, "project-1.2.3", "src", "main.c"), "int main(void){return 0;}")

	archivePath := filepath.Join(t.TempDir(), "release.tar.gz")
	if err := archive.Create(archivePath, archive.Unknown, src, []string{"."}); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if err := archive.Extract(archivePath, dest, 1); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(dest, "README"), "hello")
	assertFile(t, filepath.Join(dest, "src", "main.c"), "int main(void){return 0;}")
	if _, err := os.Stat(filepath.Join(dest, "project-1.2.3")); err == nil {
		t.Error("the stripped component is still there")
	}
}

// TestExtractRefusesToEscape is the one that matters. A tar entry naming a
// path outside the destination must not be written there.
func TestExtractRefusesToEscape(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "evil.tar")
	writeTarWithNames(t, archivePath, map[string]string{
		"../escaped.txt":             "should not be written",
		"sub/../../also-escaped.txt": "should not be written",
		"good.txt":                   "fine",
	})

	dest := filepath.Join(dir, "dest")
	if err := archive.Extract(archivePath, dest, 0); err != nil {
		t.Fatalf("extract: %v", err)
	}

	// The harmless entry landed.
	assertFile(t, filepath.Join(dest, "good.txt"), "fine")

	// Nothing landed beside the destination.
	for _, escaped := range []string{"escaped.txt", "also-escaped.txt"} {
		if _, err := os.Stat(filepath.Join(dir, escaped)); err == nil {
			t.Errorf("an archive entry escaped the destination: %s", escaped)
		}
	}
}

// TestExtractRefusesAbsolutePaths covers the other way out.
func TestExtractRefusesAbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "absolute-marker.txt")
	archivePath := filepath.Join(dir, "abs.tar")
	writeTarWithNames(t, archivePath, map[string]string{
		filepath.ToSlash(marker): "should not be written",
		"ok.txt":                 "fine",
	})

	dest := filepath.Join(dir, "dest")
	if err := archive.Extract(archivePath, dest, 0); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("an absolute entry was written outside the destination")
	}
	assertFile(t, filepath.Join(dest, "ok.txt"), "fine")
}

func TestDetectFormat(t *testing.T) {
	cases := map[string]archive.Format{
		"x.tar":    archive.Tar,
		"x.tar.gz": archive.TarGz,
		"x.tgz":    archive.TarGz,
		"X.ZIP":    archive.Zip,
		"x.7z":     archive.Unknown,
		"x":        archive.Unknown,
	}
	for name, want := range cases {
		if got := archive.DetectFormat(name); got != want {
			t.Errorf("DetectFormat(%q) = %q, want %q", name, got, want)
		}
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("%s: %v", path, err)
		return
	}
	if strings.TrimSpace(string(data)) != want {
		t.Errorf("%s = %q, want %q", path, data, want)
	}
}
