package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/vertex-language/go-cmake/run"
)

// FetchContent is how a modern project states its dependencies.
//
// The shape is deliberately simple: a declaration says where something lives, a
// later call makes it available, and what "available" means is that the
// dependency's own CMakeLists.txt has been added as a subdirectory. That last
// part is the whole trick -- a fetched dependency is not linked in some special
// way, it is just another directory of the same project.
//
// Two things here are not in CMake's version and are worth naming. A populated
// dependency records what it was populated from, so a changed tag re-fetches
// and an unchanged one does not; CMake keeps the same information in a stamp
// directory. And FETCHCONTENT_SOURCE_DIR_<NAME> is honoured before anything
// else, because pointing a build at a local checkout is how anyone actually
// works on a dependency.

func init() {
	register("fetchcontent_declare", cmdFetchContentDeclare)
	register("fetchcontent_makeavailable", cmdFetchContentMakeAvailable)
	register("fetchcontent_populate", cmdFetchContentPopulate)
	register("fetchcontent_getproperties", cmdFetchContentGetProperties)
	register("fetchcontent_setpopulated", cmdFetchContentSetPopulated)
}

// Content is one declared dependency.
type Content struct {
	Name string

	GitRepository string
	GitTag        string
	GitShallow    bool

	URL     string
	URLHash string

	SourceDir    string // an explicit checkout, which skips fetching entirely
	BinaryDir    string
	SourceSubdir string

	ExcludeFromAll bool
	System         bool
}

func cmdFetchContentDeclare(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("FetchContent_Declare called with incorrect number of arguments")
	}
	c := Content{Name: v[0]}

	keyword := ""
	for i := 1; i < len(v); i++ {
		switch v[i] {
		case "GIT_REPOSITORY", "GIT_TAG", "GIT_SHALLOW", "GIT_SUBMODULES",
			"GIT_PROGRESS", "GIT_REMOTE_NAME", "URL", "URL_HASH", "URL_MD5",
			"SOURCE_DIR", "BINARY_DIR", "SOURCE_SUBDIR", "DOWNLOAD_DIR",
			"FIND_PACKAGE_ARGS", "PATCH_COMMAND", "UPDATE_COMMAND",
			"DOWNLOAD_EXTRACT_TIMESTAMP", "DOWNLOAD_NO_EXTRACT":
			keyword = v[i]
			continue
		case "EXCLUDE_FROM_ALL":
			c.ExcludeFromAll = true
			continue
		case "SYSTEM", "OVERRIDE_FIND_PACKAGE":
			c.System = true
			continue
		}
		switch keyword {
		case "GIT_REPOSITORY":
			c.GitRepository = v[i]
		case "GIT_TAG":
			c.GitTag = v[i]
		case "GIT_SHALLOW":
			c.GitShallow = IsOn(v[i])
		case "URL":
			// Only the first URL is used; the rest are mirrors, and trying them
			// in turn would need a failure policy this does not have.
			if c.URL == "" {
				c.URL = v[i]
			}
		case "URL_HASH":
			c.URLHash = v[i]
		case "URL_MD5":
			c.URLHash = "MD5=" + v[i]
		case "SOURCE_DIR":
			c.SourceDir = e.state.absPath(v[i])
		case "BINARY_DIR":
			c.BinaryDir = e.state.absPath(v[i])
		case "SOURCE_SUBDIR":
			c.SourceSubdir = v[i]
		}
		keyword = ""
	}

	if e.state.Content == nil {
		e.state.Content = map[string]*Content{}
	}
	// A second declaration of the same name loses to the first. That is what
	// lets a top-level project pin a version its dependencies also declare,
	// which is the only reason the rule exists.
	key := strings.ToLower(c.Name)
	if _, exists := e.state.Content[key]; !exists {
		e.state.Content[key] = &c
	}
	return nil
}

func cmdFetchContentMakeAvailable(ctx context.Context, e *evaluator, args []Arg) error {
	for _, name := range Args(args) {
		populated, err := e.populate(ctx, name)
		if err != nil {
			return err
		}
		if !populated {
			continue
		}
		c := e.state.Content[strings.ToLower(name)]
		source := e.state.GetVar(strings.ToLower(name) + "_SOURCE_DIR")
		if c.SourceSubdir != "" {
			source = joinPath(source, c.SourceSubdir)
		}
		listFile := joinPath(source, "CMakeLists.txt")
		if fi, err := e.fs.Stat(listFile); err != nil || fi.IsDir() {
			// A dependency with no CMakeLists.txt is still populated; it is
			// just not a project. Header-only archives are fetched this way.
			continue
		}
		binary := e.state.GetVar(strings.ToLower(name) + "_BINARY_DIR")
		if err := e.addFetchedSubdirectory(ctx, source, binary); err != nil {
			return err
		}
	}
	return nil
}

// addFetchedSubdirectory brings a populated dependency into the build the same
// way any other directory enters it.
func (e *evaluator) addFetchedSubdirectory(ctx context.Context, source, binary string) error {
	e.state.PushDir(source, binary)
	defer e.state.PopDir()
	err := e.evalFile(ctx, joinPath(source, "CMakeLists.txt"))
	if _, isReturn := err.(returnSignal); isReturn {
		return nil
	}
	return err
}

func cmdFetchContentPopulate(ctx context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("FetchContent_Populate called with incorrect number of arguments")
	}
	_, err := e.populate(ctx, v[0])
	return err
}

func cmdFetchContentGetProperties(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("FetchContent_GetProperties called with incorrect number of arguments")
	}
	name := strings.ToLower(v[0])

	// With no output variables named, the properties land under the standard
	// lower-cased names, which is how the documented if(NOT <name>_POPULATED)
	// idiom sees them.
	sourceVar, binaryVar, populatedVar := name+"_SOURCE_DIR", name+"_BINARY_DIR", name+"_POPULATED"
	for i := 1; i+1 < len(v); i += 2 {
		switch v[i] {
		case "SOURCE_DIR":
			sourceVar = v[i+1]
		case "BINARY_DIR":
			binaryVar = v[i+1]
		case "POPULATED":
			populatedVar = v[i+1]
		}
	}
	e.state.SetVar(sourceVar, e.state.GetVar(name+"_SOURCE_DIR"))
	e.state.SetVar(binaryVar, e.state.GetVar(name+"_BINARY_DIR"))
	e.state.SetVar(populatedVar, e.state.GetVar(name+"_POPULATED"))
	return nil
}

func cmdFetchContentSetPopulated(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("FetchContent_SetPopulated called with incorrect number of arguments")
	}
	name := strings.ToLower(v[0])
	for i := 1; i+1 < len(v); i += 2 {
		switch v[i] {
		case "SOURCE_DIR":
			e.state.SetVar(name+"_SOURCE_DIR", v[i+1])
		case "BINARY_DIR":
			e.state.SetVar(name+"_BINARY_DIR", v[i+1])
		}
	}
	e.state.SetVar(name+"_POPULATED", "TRUE")
	return nil
}

// populate makes a declared dependency present on disk and reports whether it
// is now available.
func (e *evaluator) populate(ctx context.Context, name string) (bool, error) {
	key := strings.ToLower(name)
	c, ok := e.state.Content[key]
	if !ok {
		return false, e.fatalf("FetchContent: %s was never declared", name)
	}
	if IsOn(e.state.GetVar(key + "_POPULATED")) {
		return true, nil
	}

	// A local checkout wins over anything remote. Someone debugging a
	// dependency points this at their working tree and expects the build to use
	// it unchanged -- re-fetching over the top would discard their edits.
	if override := e.state.GetVar("FETCHCONTENT_SOURCE_DIR_" + strings.ToUpper(name)); override != "" {
		return e.markPopulated(key, override, e.contentBinaryDir(c, key)), nil
	}
	if c.SourceDir != "" {
		return e.markPopulated(key, c.SourceDir, e.contentBinaryDir(c, key)), nil
	}

	source := joinPath(e.fetchBaseDir(), key+"-src")
	binary := e.contentBinaryDir(c, key)

	// The stamp records what the directory holds. Re-fetching an unchanged
	// dependency wastes a minute on every configure; not re-fetching a changed
	// one builds the wrong code, and the second failure is far harder to see.
	want := contentStamp(c)
	if e.stampMatches(source, want) {
		return e.markPopulated(key, source, binary), nil
	}

	e.state.log("STATUS", "Populating "+c.Name)
	switch {
	case c.GitRepository != "":
		if err := e.fetchGit(ctx, c, source); err != nil {
			return false, e.fatalf("FetchContent(%s): %v", c.Name, err)
		}
	case c.URL != "":
		if err := e.fetchURL(ctx, c, source); err != nil {
			return false, e.fatalf("FetchContent(%s): %v", c.Name, err)
		}
	default:
		return false, e.fatalf("FetchContent(%s) has neither GIT_REPOSITORY nor URL", c.Name)
	}
	e.writeStamp(source, want)
	return e.markPopulated(key, source, binary), nil
}

func (e *evaluator) markPopulated(key, source, binary string) bool {
	e.state.SetVar(key+"_SOURCE_DIR", source)
	e.state.SetVar(key+"_BINARY_DIR", binary)
	e.state.SetVar(key+"_POPULATED", "TRUE")
	return true
}

// fetchBaseDir is where populated dependencies live.
func (e *evaluator) fetchBaseDir() string {
	if base := e.state.GetVar("FETCHCONTENT_BASE_DIR"); base != "" {
		return base
	}
	return joinPath(e.state.BinaryDir, "_deps")
}

func (e *evaluator) contentBinaryDir(c *Content, key string) string {
	if c.BinaryDir != "" {
		return c.BinaryDir
	}
	return joinPath(e.fetchBaseDir(), key+"-build")
}

// fetchGit clones a repository at a tag.
func (e *evaluator) fetchGit(ctx context.Context, c *Content, source string) error {
	if e.state.Runner == nil {
		return fmt.Errorf("cloning requires a process runner, which this configuration does not provide")
	}
	// A previous checkout is removed rather than updated in place: the tag may
	// have moved, the remote may have been rewritten, and a half-updated tree
	// builds something no revision ever contained.
	if err := e.fs.RemoveAll(source); err != nil {
		return err
	}
	if err := e.fs.MkdirAll(dirOf(source)); err != nil {
		return err
	}

	clone := []string{"git", "clone"}
	if c.GitShallow && c.GitTag != "" {
		// A shallow clone can only be pinned to something the remote advertises,
		// which a tag or branch is and a commit is not.
		clone = append(clone, "--depth", "1", "--branch", c.GitTag)
	}
	clone = append(clone, "--recurse-submodules", c.GitRepository, source)
	if err := e.runGit(ctx, clone, ""); err != nil {
		return err
	}
	if c.GitTag != "" && !c.GitShallow {
		if err := e.runGit(ctx, []string{"git", "checkout", "--detach", c.GitTag}, source); err != nil {
			return err
		}
		if err := e.runGit(ctx, []string{"git", "submodule", "update", "--init", "--recursive"}, source); err != nil {
			// A repository with no submodules fails this harmlessly.
			_ = err
		}
	}
	return nil
}

func (e *evaluator) runGit(ctx context.Context, argv []string, dir string) error {
	var out bytes.Buffer
	code, err := e.state.Runner.Run(ctx, run.Command{
		Argv:   argv,
		Dir:    dir,
		Stdout: &out,
		Stderr: &out,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	if code != 0 {
		return fmt.Errorf("%s: exit status %d\n%s", strings.Join(argv, " "), code,
			strings.TrimSpace(out.String()))
	}
	return nil
}

// fetchURL downloads an archive and unpacks it.
func (e *evaluator) fetchURL(ctx context.Context, c *Content, source string) error {
	if e.state.Downloader == nil {
		return fmt.Errorf("%v", ErrNoDownloader)
	}
	base := e.fetchBaseDir()
	if err := e.fs.MkdirAll(base); err != nil {
		return err
	}
	archivePath := joinPath(base, strings.ToLower(c.Name)+"-download"+urlExtension(c.URL))

	res, err := e.state.Downloader.Download(ctx, DownloadRequest{
		URL:          c.URL,
		Dest:         archivePath,
		ExpectedHash: c.URLHash,
		TLSVerify:    true,
	})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("downloading %s: %s", c.URL, res.Message)
	}

	if err := e.fs.RemoveAll(source); err != nil {
		return err
	}
	if e.state.Extractor == nil {
		return fmt.Errorf("this configuration cannot unpack archives")
	}
	// One leading component is stripped: a release tarball's contents sit under
	// a versioned directory that nothing downstream should have to name.
	return e.state.Extractor.Extract(archivePath, source, 1)
}

// urlExtension keeps the archive's suffix so its format can be told from its
// name, which is how the extractor decides what it is holding.
func urlExtension(url string) string {
	if i := strings.IndexAny(url, "?#"); i >= 0 {
		url = url[:i]
	}
	name := BaseName(url)
	for _, ext := range []string{".tar.gz", ".tgz", ".tar", ".zip"} {
		if strings.HasSuffix(strings.ToLower(name), ext) {
			return ext
		}
	}
	return ".archive"
}

// contentStamp is what a populated directory was populated from.
func contentStamp(c *Content) string {
	h := sha256.New()
	fmt.Fprintln(h, c.GitRepository, c.GitTag, c.GitShallow)
	fmt.Fprintln(h, c.URL, c.URLHash)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

const stampName = ".go-cmake-fetch-stamp"

func (e *evaluator) stampMatches(source, want string) bool {
	data, err := e.fs.ReadFile(joinPath(source, stampName))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == want
}

func (e *evaluator) writeStamp(source, stamp string) {
	_ = e.fs.WriteFile(joinPath(source, stampName), []byte(stamp+"\n"))
}

// Extractor unpacks an archive. It is an interface for the same reason the
// downloader is: an archive off the network is untrusted input, and a caller
// may want to inspect or refuse one rather than have it unpacked for them.
type Extractor interface {
	Extract(archivePath, dest string, stripComponents int) error
}
