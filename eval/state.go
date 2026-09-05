package eval

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/vertex-language/go-cmake/ast"
	"github.com/vertex-language/go-cmake/run"
	"github.com/vertex-language/go-cmake/token"
)

// State holds the complete mutable state of the CMake configure phase.
type State struct {
	Root    *Scope
	Current *Scope
	Cache   *Cache
	Env     map[string]string // environment variables (modifiable, not written back)

	// Policies: map of policy ID ("CMP0001" etc.) to "NEW", "OLD", or "WARN".
	Policies map[string]string

	// Properties: global, directory, and target properties.
	// Key format: "GLOBAL:<name>", "DIRECTORY:<dir>:<name>", "TARGET:<target>:<name>", etc.
	Properties map[string]string

	// Registered targets (populated by add_library, add_executable, etc.).
	Targets map[string]*TargetState

	// Source and binary directories.
	SourceDir string
	BinaryDir string

	// For error reporting.
	File string
	Line int

	// For macro/function definitions.
	Macros    map[string]*MacroDef
	Functions map[string]*FunctionDef

	// Guard for include_guard(): set of files already included.
	// "GLOBAL" key means all files; "DIRECTORY" key means per-directory.
	IncludeGuards map[string]bool

	// Log sink for message() output (defaults to os.Stderr if nil, but tests inject it).
	LogSink func(mode, text string)

	// Stack of policy scopes pushed by cmake_policy(PUSH).
	PolicyStack []map[string]string

	// Tests registered via add_test.
	Tests []TestEntry

	// Enabled languages.
	Languages map[string]bool

	// FileSet resolves token positions to file:line for diagnostics.
	FileSet *token.FileSet

	// callDepth guards against runaway recursion in functions and macros.
	callDepth int

	// Dirs is the directory stack maintained by add_subdirectory. The last
	// entry is the directory currently being processed.
	Dirs []*Directory

	// Order in which directories were entered, for generation.
	AllDirs []*Directory

	// TargetOrder records declaration order, which decides link order and the
	// order targets appear in generated build files.
	TargetOrder []string

	// Includes is the stack of files currently being evaluated, used to build
	// CMAKE_CURRENT_LIST_FILE and to detect include cycles.
	Includes []string

	// Errors collects message(SEND_ERROR) text. Configure continues after a
	// send-error but must not report success.
	Errors []string

	// DefinedProps records the properties declared with define_property.
	DefinedProps map[string]bool

	// ScriptMode is set when the language is being evaluated by `cmake -P`,
	// where there is no project for a project-declaring command to act on.
	ScriptMode bool

	// TestingEnabled is set by enable_testing().
	TestingEnabled bool

	// InstallRules collects install() directives in declaration order.
	InstallRules []InstallRule

	// CustomCommands collects add_custom_command(OUTPUT ...) rules.
	CustomCommands []CustomCommand

	// ConfiguredFiles lists every file configure_file produced, so the build
	// can depend on them and a re-configure can clean them up.
	ConfiguredFiles []string

	// GlobDepends holds the patterns of file(GLOB ... CONFIGURE_DEPENDS).
	GlobDepends []string

	// GeneratedFiles holds file(GENERATE) requests, deferred to generate time.
	GeneratedFiles []GeneratedFile

	// Runner executes processes for execute_process. A nil Runner makes those
	// commands report an error rather than silently succeed.
	Runner run.Runner

	// Compiler answers try_compile. A nil Compiler makes the probe report that
	// it could not be answered, which is the only honest result: a project told
	// its compiler lacks a feature it has will disable that feature, and the
	// build that follows is wrong in a way nothing points at.
	Compiler Compiler

	// Downloader fetches what a project declares it needs. A nil Downloader
	// makes file(DOWNLOAD) and FetchContent refuse rather than reach the
	// network, which is the right default for a library: the decision to make
	// a request belongs to the program embedding this one.
	Downloader Downloader

	// Extractor unpacks a fetched archive.
	Extractor Extractor

	// Content holds what FetchContent_Declare recorded, keyed by lower-cased
	// name because every FetchContent command is case-insensitive about it.
	Content map[string]*Content

	// Unsupported records commands that were accepted but could not be honoured,
	// so a caller can tell an incomplete configure from a complete one.
	Unsupported []string
}

// GeneratedFile is a deferred file(GENERATE) request. Its content may contain
// generator expressions, so it cannot be written until the target graph is
// complete.
type GeneratedFile struct {
	Output    string
	Content   string
	Input     string
	Condition string
	SourceDir string
	BinaryDir string
}

// InstallRule is one install() directive.
type InstallRule struct {
	Kind        string // TARGETS, FILES, PROGRAMS, DIRECTORY, SCRIPT, CODE, EXPORT
	Items       []string
	Destination string
	Component   string
	Permissions []string
	Optional    bool
	Rename      string
	SourceDir   string
}

// CustomCommand is an add_custom_command(OUTPUT ...) rule: the build-graph
// primitive that every generated build file ultimately reduces to.
type CustomCommand struct {
	Outputs    []string
	Byproducts []string
	Depends    []string
	Commands   [][]string
	Comment    string
	WorkDir    string
	Verbatim   bool
	SourceDir  string
}

// Directory is one directory of the project: a CMakeLists.txt, the variable
// scope it introduced, and the properties attached to it.
type Directory struct {
	Source     string
	Binary     string
	Scope      *Scope
	Properties map[string]string
	Parent     *Directory
	Children   []*Directory

	// Directory-level compile settings, inherited by targets declared here and
	// by subdirectories added from here. CMake snapshots these at the point
	// add_subdirectory runs, which is why a later include_directories() in the
	// parent does not reach a child that was already added.
	IncludeDirs []string
	Definitions []string
	Options     []string
	LinkOptions []string
	LinkDirs    []string
	LinkLibs    []string

	// Deferred calls recorded by cmake_language(DEFER CALL ...).
	Deferred [][]string
}

// inherit copies the settings a subdirectory starts life with from its parent.
func (d *Directory) inherit(p *Directory) {
	if p == nil {
		return
	}
	d.IncludeDirs = append([]string{}, p.IncludeDirs...)
	d.Definitions = append([]string{}, p.Definitions...)
	d.Options = append([]string{}, p.Options...)
	d.LinkOptions = append([]string{}, p.LinkOptions...)
	d.LinkDirs = append([]string{}, p.LinkDirs...)
	d.LinkLibs = append([]string{}, p.LinkLibs...)
}

// TestEntry holds a registered test.
type TestEntry struct {
	Name    string
	Command []string

	// WorkDir is where the test runs, which WORKING_DIRECTORY may move away
	// from the directory that declared it.
	WorkDir string

	// BinaryDir is the directory that declared the test. It decides which
	// generated test file the test is written into, and so which subtree a
	// `ctest` run from a subdirectory will find it in.
	BinaryDir string

	Configurations []string
}

// TargetState holds information about a CMake target.
//
// The three usage-requirement scopes are not stored as three lists per
// property. CMake keeps two: the build-side list (what compiling this target
// needs) and the interface list (what compiling a consumer needs). PRIVATE
// writes the first, INTERFACE the second, and PUBLIC both. Storing it this way
// rather than as a scope tag per entry is what makes propagation a matter of
// concatenating interface lists rather than re-interpreting scopes at use time.
type TargetState struct {
	Name        string
	Type        string // STATIC, SHARED, MODULE, OBJECT, INTERFACE, EXECUTABLE, UTILITY, ALIAS
	AliasOf     string // for ALIAS targets
	Imported    bool
	GlobalScope bool
	ExcludeAll  bool

	// The directory that declared this target.
	SourceDir string
	BinaryDir string

	// Source files, already made absolute.
	Sources []string

	// Properties stored as a map; the generator reads these.
	Properties map[string]string

	// Build-side usage requirements: what this target itself needs.
	IncludeDirs  []string
	Defines      []string
	CompileOpts  []string
	CompileFeats []string
	LinkOpts     []string
	LinkDirs     []string
	LinkLibs     []string

	// Interface usage requirements: what a consumer of this target inherits.
	IfaceIncludeDirs  []string
	IfaceDefines      []string
	IfaceCompileOpts  []string
	IfaceCompileFeats []string
	IfaceLinkOpts     []string
	IfaceLinkDirs     []string
	IfaceLinkLibs     []string

	// Custom commands attached to this target.
	PreBuild  [][]string
	PreLink   [][]string
	PostBuild [][]string

	// For add_custom_target: the commands it runs.
	Commands [][]string
	Depends  []string
}

// Property returns a target property, consulting the fields that back the
// well-known properties before the generic map.
func (t *TargetState) Property(name string) (string, bool) {
	switch name {
	case "NAME":
		return t.Name, true
	case "TYPE":
		return t.TypeName(), true
	case "SOURCES":
		return listProperty(t.Sources)
	case "INCLUDE_DIRECTORIES":
		return listProperty(t.IncludeDirs)
	case "INTERFACE_INCLUDE_DIRECTORIES":
		return listProperty(t.IfaceIncludeDirs)
	case "COMPILE_DEFINITIONS":
		return listProperty(t.Defines)
	case "INTERFACE_COMPILE_DEFINITIONS":
		return listProperty(t.IfaceDefines)
	case "COMPILE_OPTIONS":
		return listProperty(t.CompileOpts)
	case "INTERFACE_COMPILE_OPTIONS":
		return listProperty(t.IfaceCompileOpts)
	case "COMPILE_FEATURES":
		return listProperty(t.CompileFeats)
	case "INTERFACE_COMPILE_FEATURES":
		return listProperty(t.IfaceCompileFeats)
	case "LINK_LIBRARIES":
		return listProperty(t.LinkLibs)
	case "INTERFACE_LINK_LIBRARIES":
		return listProperty(t.IfaceLinkLibs)
	case "LINK_OPTIONS":
		return listProperty(t.LinkOpts)
	case "INTERFACE_LINK_OPTIONS":
		return listProperty(t.IfaceLinkOpts)
	case "LINK_DIRECTORIES":
		return listProperty(t.LinkDirs)
	case "INTERFACE_LINK_DIRECTORIES":
		return listProperty(t.IfaceLinkDirs)
	case "IMPORTED":
		return boolVar(t.Imported), true
	case "ALIASED_TARGET":
		if t.AliasOf == "" {
			return "", false
		}
		return t.AliasOf, true
	}
	v, ok := t.Properties[name]
	return v, ok
}

// listProperty reports a list-backed property. An empty list is an unset
// property, not an empty one: get_target_property must yield <var>-NOTFOUND so
// that `if(prop)` is false rather than true-because-the-string-exists.
func listProperty(v []string) (string, bool) {
	if len(v) == 0 {
		return "", false
	}
	return JoinList(v), true
}

// TypeName renders the target's kind the way the TYPE property reports it.
// An executable and a custom target are not libraries, so the "_LIBRARY"
// suffix belongs only to the library kinds.
func (t *TargetState) TypeName() string {
	switch t.Type {
	case "EXECUTABLE":
		return "EXECUTABLE"
	case "UTILITY":
		return "UTILITY"
	case "ALIAS":
		return "ALIAS"
	default:
		return t.Type + "_LIBRARY"
	}
}

// SetProperty stores a target property, routing the well-known ones to their
// backing field so that a set_target_properties call and a target_* call write
// the same place.
func (t *TargetState) SetProperty(name, value string) {
	switch name {
	case "SOURCES":
		t.Sources = SplitList(value)
	case "INCLUDE_DIRECTORIES":
		t.IncludeDirs = SplitList(value)
	case "INTERFACE_INCLUDE_DIRECTORIES":
		t.IfaceIncludeDirs = SplitList(value)
	case "COMPILE_DEFINITIONS":
		t.Defines = SplitList(value)
	case "INTERFACE_COMPILE_DEFINITIONS":
		t.IfaceDefines = SplitList(value)
	case "COMPILE_OPTIONS":
		t.CompileOpts = SplitList(value)
	case "INTERFACE_COMPILE_OPTIONS":
		t.IfaceCompileOpts = SplitList(value)
	case "LINK_LIBRARIES":
		t.LinkLibs = SplitList(value)
	case "INTERFACE_LINK_LIBRARIES":
		t.IfaceLinkLibs = SplitList(value)
	case "LINK_OPTIONS":
		t.LinkOpts = SplitList(value)
	default:
		if t.Properties == nil {
			t.Properties = map[string]string{}
		}
		t.Properties[name] = value
	}
}

// MacroDef holds the definition of a CMake macro.
type MacroDef struct {
	Name   string
	Params []string
	Body   []ast.Stmt
}

// FunctionDef holds the definition of a CMake function.
type FunctionDef struct {
	Name   string
	Params []string
	Body   []ast.Stmt
}

// NewState creates a new State with root scope and populated environment.
func NewState(sourceDir, binaryDir string, env []string) *State {
	root := NewScope(DirectoryScope, nil)
	s := &State{
		Root:          root,
		Current:       root,
		Cache:         NewCache(),
		Env:           make(map[string]string),
		Policies:      make(map[string]string),
		Properties:    make(map[string]string),
		Targets:       make(map[string]*TargetState),
		Macros:        make(map[string]*MacroDef),
		Functions:     make(map[string]*FunctionDef),
		IncludeGuards: make(map[string]bool),
		DefinedProps:  make(map[string]bool),
		Languages:     make(map[string]bool),
		SourceDir:     sourceDir,
		BinaryDir:     binaryDir,
	}
	// Populate environment.
	if len(env) == 0 {
		env = os.Environ()
	}
	for _, e := range env {
		if idx := strings.IndexByte(e, '='); idx >= 0 {
			s.Env[e[:idx]] = e[idx+1:]
		}
	}
	// The root directory shares the root scope: a variable set at top level
	// and a variable set in the top-level CMakeLists.txt are the same variable.
	rootDir := &Directory{
		Source:     sourceDir,
		Binary:     binaryDir,
		Scope:      root,
		Properties: map[string]string{},
	}
	s.Dirs = []*Directory{rootDir}
	s.AllDirs = []*Directory{rootDir}

	// Set built-in variables.
	root.Set("CMAKE_SOURCE_DIR", sourceDir)
	root.Set("CMAKE_BINARY_DIR", binaryDir)
	root.Set("CMAKE_CURRENT_SOURCE_DIR", sourceDir)
	root.Set("CMAKE_CURRENT_BINARY_DIR", binaryDir)
	root.Set("CMAKE_VERSION", "4.4.3")
	root.Set("CMAKE_MAJOR_VERSION", "4")
	root.Set("CMAKE_MINOR_VERSION", "4")
	root.Set("CMAKE_PATCH_VERSION", "3")
	root.Set("CMAKE_TWEAK_VERSION", "0")
	root.Set("TRUE", "TRUE")
	root.Set("FALSE", "FALSE")
	root.Set("ON", "ON")
	root.Set("OFF", "OFF")
	root.Set("YES", "YES")
	root.Set("NO", "NO")
	root.Set("CMAKE_FILES_DIRECTORY", "/CMakeFiles")
	root.Set("CMAKE_COMMAND", "cmake")
	root.Set("CMAKE_HOST_SYSTEM_NAME", hostSystemName())
	root.Set("CMAKE_HOST_WIN32", boolVar(runtime.GOOS == "windows"))
	root.Set("CMAKE_HOST_UNIX", boolVar(runtime.GOOS != "windows"))
	root.Set("CMAKE_HOST_APPLE", boolVar(runtime.GOOS == "darwin"))
	root.Set("UNIX", boolVar(runtime.GOOS != "windows"))
	root.Set("WIN32", boolVar(runtime.GOOS == "windows"))
	root.Set("APPLE", boolVar(runtime.GOOS == "darwin"))
	root.Set("CMAKE_SYSTEM_NAME", hostSystemName())
	root.Set("CMAKE_SIZEOF_VOID_P", "8")
	return s
}

// hostSystemName reports the value CMake uses for CMAKE_HOST_SYSTEM_NAME.
func hostSystemName() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows"
	case "darwin":
		return "Darwin"
	case "linux":
		return "Linux"
	case "freebsd":
		return "FreeBSD"
	case "openbsd":
		return "OpenBSD"
	case "netbsd":
		return "NetBSD"
	default:
		return strings.ToUpper(runtime.GOOS[:1]) + runtime.GOOS[1:]
	}
}

// boolDigit renders a Go bool as "1"/"0". Queries that answer a yes/no
// question -- get_property(... SET), for one -- use this form rather than the
// empty-string form, because the caller may print the answer as well as test it.
func boolDigit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// boolVar renders a Go bool the way CMake stores a platform flag: "1" when
// true and unset-looking empty string when false, because `if(WIN32)` must be
// false on other platforms and CMake spells that as an empty value.
func boolVar(b bool) string {
	if b {
		return "1"
	}
	return ""
}

// Lookup implements expr.Lookup: kind is "normal", "env", or "cache".
func (s *State) Lookup(kind, name string) (string, bool) {
	switch kind {
	case "env":
		v, ok := s.Env[name]
		return v, ok
	case "cache":
		if e, ok := s.Cache.Get(name); ok {
			return e.Value, true
		}
		return "", false
	default: // "normal"
		if v, ok := s.Current.Get(name); ok {
			return v, true
		}
		// Fall through to cache.
		if e, ok := s.Cache.Get(name); ok {
			return e.Value, true
		}
		return "", false
	}
}

// SetVar sets a variable in the current scope.
func (s *State) SetVar(name, value string) {
	s.Current.Set(name, value)
}

// UnsetVar removes a variable from the current scope.
func (s *State) UnsetVar(name string) {
	s.Current.Unset(name)
}

// GetVar retrieves a variable, returning "" if not set.
func (s *State) GetVar(name string) string {
	v, _ := s.Lookup("normal", name)
	return v
}

// ----------------------------------------------------------------------------
// Position tracking

// setPosition records the source position of the command being evaluated so
// that a later error can name the file and line that raised it.
func (s *State) setPosition(c *ast.CommandInvocation) {
	if s.FileSet == nil {
		return
	}
	pos := s.FileSet.Position(c.Pos())
	if pos.IsValid() {
		s.File = pos.Filename
		s.Line = pos.Line
	}
}

// log emits a message() line through the sink, or to stdout if none is set.
func (s *State) log(mode, text string) {
	if s.LogSink != nil {
		s.LogSink(mode, text)
		return
	}
	switch mode {
	case "":
		fmt.Println(text)
	case "STATUS":
		fmt.Println("-- " + text)
	default:
		fmt.Printf("%s: %s\n", mode, text)
	}
}

// ----------------------------------------------------------------------------
// Directories

// RootDir returns the top-level directory of the tree.
func (s *State) RootDir() *Directory {
	if len(s.AllDirs) == 0 {
		return nil
	}
	return s.AllDirs[0]
}

// Dir returns the directory currently being evaluated.
func (s *State) Dir() *Directory {
	if len(s.Dirs) == 0 {
		return nil
	}
	return s.Dirs[len(s.Dirs)-1]
}

// PushDir enters a subdirectory: a new variable scope is created as a child of
// the current one, so the subdirectory sees its parent's variables but its own
// assignments do not leak back out.
func (s *State) PushDir(source, binary string) *Directory {
	parent := s.Dir()
	d := &Directory{
		Source:     source,
		Binary:     binary,
		Scope:      NewScope(DirectoryScope, s.Current),
		Properties: map[string]string{},
		Parent:     parent,
	}
	d.inherit(parent)
	if parent != nil {
		parent.Children = append(parent.Children, d)
	}
	s.Dirs = append(s.Dirs, d)
	s.AllDirs = append(s.AllDirs, d)
	s.Current = d.Scope
	s.Current.Set("CMAKE_CURRENT_SOURCE_DIR", source)
	s.Current.Set("CMAKE_CURRENT_BINARY_DIR", binary)
	return d
}

// PopDir leaves the current subdirectory and restores the parent scope.
func (s *State) PopDir() {
	if len(s.Dirs) <= 1 {
		return
	}
	s.Dirs = s.Dirs[:len(s.Dirs)-1]
	s.Current = s.Dir().Scope
}

// AddTarget registers a target, preserving declaration order.
func (s *State) AddTarget(t *TargetState) {
	if _, exists := s.Targets[t.Name]; !exists {
		s.TargetOrder = append(s.TargetOrder, t.Name)
	}
	s.Targets[t.Name] = t
}

// Target resolves a target name, following one level of ALIAS indirection.
func (s *State) Target(name string) (*TargetState, bool) {
	t, ok := s.Targets[name]
	if !ok {
		return nil, false
	}
	if t.Type == "ALIAS" && t.AliasOf != "" {
		if real, ok := s.Targets[t.AliasOf]; ok {
			return real, true
		}
	}
	return t, true
}

// FindDirectory locates a directory of the tree by its source path. The
// generator needs it to recover the directory-level settings that a target did
// not copy at declaration time.
func (s *State) FindDirectory(source string) *Directory {
	return s.findDir(source)
}
