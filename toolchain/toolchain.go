// Package toolchain discovers the compilers and archivers a build will use and
// describes how to drive them.
//
// CMake performs this discovery during project(), by compiling a test program
// and reading the result. That is a build-phase effect happening in the
// configure phase, and it is why a CMake configure needs a working compiler
// even to answer a question about the project's structure. This package keeps
// the discovery separate and injectable, so a caller can supply a known
// toolchain description instead and configure a project for a machine that is
// not the one running.
package toolchain

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Kind is the command-line convention a compiler follows. It is not the same
// question as which vendor wrote the compiler: clang-cl speaks MSVC's flags,
// and that is what the generator needs to know.
type Kind int

const (
	Unknown Kind = iota
	GNU          // gcc, clang, and anything accepting -o/-I/-D
	MSVC         // cl.exe and clang-cl, accepting /Fo /I /D
)

func (k Kind) String() string {
	switch k {
	case GNU:
		return "GNU"
	case MSVC:
		return "MSVC"
	default:
		return "Unknown"
	}
}

// Compiler describes one language's compiler.
type Compiler struct {
	Language string // "C" or "CXX"
	Path     string // absolute path to the executable
	ID       string // CMAKE_<LANG>_COMPILER_ID: GNU, Clang, MSVC, AppleClang
	Version  string // CMAKE_<LANG>_COMPILER_VERSION, when it can be determined
	Kind     Kind
}

// Toolchain is everything the generator needs to write compile and link rules.
type Toolchain struct {
	Compilers map[string]Compiler // keyed by language
	Archiver  string              // ar or lib.exe
	Linker    string              // link.exe on MSVC; the compiler drives linking elsewhere

	// MSVC is the located Visual C++ installation, when one was found off PATH.
	// Its INCLUDE and LIB directories have to reach the compiler somehow, and
	// the generator turns them into explicit flags rather than relying on the
	// build being launched from a developer command prompt.
	MSVC *MSVCInstall

	// Object, static library, shared library, and executable naming.
	ObjectSuffix string
	StaticPrefix string
	StaticSuffix string
	SharedPrefix string
	SharedSuffix string
	ExeSuffix    string

	// ImportSuffix is the link-time stub a Windows DLL produces; empty
	// elsewhere, where the shared library is linked against directly.
	ImportSuffix string
}

// Env is the environment discovery reads. It is a parameter rather than a
// direct call to os.Getenv so that a caller can configure for a environment
// other than its own.
type Env struct {
	Vars []string
	Path []string
}

// OSEnv returns the environment of the running process.
func OSEnv() Env {
	return Env{Vars: os.Environ(), Path: filepath.SplitList(os.Getenv("PATH"))}
}

func (e Env) get(name string) string {
	prefix := name + "="
	for _, v := range e.Vars {
		if strings.HasPrefix(v, prefix) {
			return v[len(prefix):]
		}
	}
	return ""
}

// Detect finds a compiler for each requested language.
//
// The search order is the one a developer expects: an explicit CC or CXX first,
// because that is how a person overrides the choice, then the conventional
// names on PATH. Detection never fails outright — a language with no compiler
// yields a Compiler with an empty Path, and the caller decides whether that is
// fatal, because configuring a project to inspect it is useful even on a
// machine that cannot build it.
func Detect(languages []string, env Env) *Toolchain {
	t := &Toolchain{Compilers: map[string]Compiler{}}
	for _, lang := range languages {
		if c, ok := findCompiler(lang, env); ok {
			t.Compilers[lang] = c
		}
	}
	// Nothing on PATH: on Windows that is the normal state, because Visual
	// Studio expects a batch file to have set it up. Look where it installs.
	if len(t.Compilers) == 0 {
		if m := FindMSVC(""); m != nil {
			t.MSVC = m
			cl := m.tool("cl")
			for _, lang := range languages {
				if lang == "C" || lang == "CXX" {
					t.Compilers[lang] = Compiler{Language: lang, Path: cl, ID: "MSVC", Kind: MSVC}
				}
			}
			t.Archiver = m.tool("lib")
			t.Linker = m.tool("link")
		}
	}
	t.applyPlatformConventions()
	if t.Archiver == "" {
		t.findBinutils(env)
	}
	return t
}

// candidates lists the executables to try for a language, in preference order.
func candidates(lang string, env Env) []string {
	var out []string
	switch lang {
	case "C":
		if v := env.get("CC"); v != "" {
			out = append(out, v)
		}
		if runtime.GOOS == "windows" {
			out = append(out, "cl", "clang-cl", "clang", "gcc")
		} else {
			out = append(out, "cc", "gcc", "clang")
		}
	case "CXX":
		if v := env.get("CXX"); v != "" {
			out = append(out, v)
		}
		if runtime.GOOS == "windows" {
			out = append(out, "cl", "clang-cl", "clang++", "g++")
		} else {
			out = append(out, "c++", "g++", "clang++")
		}
	}
	return out
}

func findCompiler(lang string, env Env) (Compiler, bool) {
	for _, name := range candidates(lang, env) {
		path := lookPath(name, env)
		if path == "" {
			continue
		}
		id, kind := identify(path)
		return Compiler{Language: lang, Path: slash(path), ID: id, Kind: kind}, true
	}
	return Compiler{}, false
}

// identify classifies a compiler from its file name. Running it with --version
// would be more certain, but it would also make configure require executing an
// arbitrary binary, and the name is reliable in every case that matters: a
// compiler that lies about its name is not one a build system can help with.
func identify(path string) (string, Kind) {
	base := strings.ToLower(filepath.Base(path))
	base = strings.TrimSuffix(base, ".exe")
	switch {
	case base == "cl":
		return "MSVC", MSVC
	case strings.HasSuffix(base, "clang-cl"):
		return "Clang", MSVC
	case strings.Contains(base, "clang"):
		if runtime.GOOS == "darwin" {
			return "AppleClang", GNU
		}
		return "Clang", GNU
	case strings.Contains(base, "gcc"), strings.Contains(base, "g++"):
		return "GNU", GNU
	case base == "cc", base == "c++":
		// The generic names are symlinks to whatever the system prefers; on a
		// mac that is Apple's clang, elsewhere it is usually GCC.
		if runtime.GOOS == "darwin" {
			return "AppleClang", GNU
		}
		return "GNU", GNU
	default:
		return "Unknown", GNU
	}
}

// lookPath finds an executable on the given search path.
func lookPath(name string, env Env) string {
	if strings.ContainsAny(name, `/\`) {
		if fi, err := os.Stat(name); err == nil && !fi.IsDir() {
			abs, _ := filepath.Abs(name)
			return abs
		}
		return ""
	}
	exts := []string{""}
	if runtime.GOOS == "windows" {
		exts = []string{".exe", ".com", ".bat", ".cmd"}
	}
	dirs := env.Path
	if len(dirs) == 0 {
		dirs = filepath.SplitList(env.get("PATH"))
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		for _, ext := range exts {
			full := filepath.Join(dir, name+ext)
			if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
				return full
			}
		}
	}
	return ""
}

// applyPlatformConventions fills in the file-naming rules, which follow the
// compiler's convention rather than the host's: a MinGW build on Windows still
// produces libfoo.a, and clang-cl still produces foo.lib.
func (t *Toolchain) applyPlatformConventions() {
	msvc := t.Kind() == MSVC
	switch {
	case msvc:
		t.ObjectSuffix = ".obj"
		t.StaticPrefix, t.StaticSuffix = "", ".lib"
		t.SharedPrefix, t.SharedSuffix = "", ".dll"
		t.ImportSuffix = ".lib"
		t.ExeSuffix = ".exe"
	case runtime.GOOS == "windows":
		// MinGW: UNIX names for archives, Windows names for what runs.
		t.ObjectSuffix = ".obj"
		t.StaticPrefix, t.StaticSuffix = "lib", ".a"
		t.SharedPrefix, t.SharedSuffix = "", ".dll"
		t.ImportSuffix = ".dll.a"
		t.ExeSuffix = ".exe"
	case runtime.GOOS == "darwin":
		t.ObjectSuffix = ".o"
		t.StaticPrefix, t.StaticSuffix = "lib", ".a"
		t.SharedPrefix, t.SharedSuffix = "lib", ".dylib"
		t.ExeSuffix = ""
	default:
		t.ObjectSuffix = ".o"
		t.StaticPrefix, t.StaticSuffix = "lib", ".a"
		t.SharedPrefix, t.SharedSuffix = "lib", ".so"
		t.ExeSuffix = ""
	}
}

func (t *Toolchain) findBinutils(env Env) {
	if t.Kind() == MSVC {
		t.Archiver = slash(lookPath("lib", env))
		t.Linker = slash(lookPath("link", env))
		return
	}
	t.Archiver = slash(lookPath("ar", env))
}

// Kind reports the command-line convention of the toolchain as a whole. A
// project mixing conventions across languages is not something CMake supports
// either, so the first compiler found decides.
func (t *Toolchain) Kind() Kind {
	for _, lang := range []string{"C", "CXX"} {
		if c, ok := t.Compilers[lang]; ok && c.Kind != Unknown {
			return c.Kind
		}
	}
	for _, c := range t.Compilers {
		if c.Kind != Unknown {
			return c.Kind
		}
	}
	return Unknown
}

// Compiler returns the compiler for a language.
func (t *Toolchain) Compiler(lang string) (Compiler, bool) {
	c, ok := t.Compilers[lang]
	return c, ok
}

// Variables renders the toolchain as the CMAKE_* cache entries a CMakeLists.txt
// expects to find after project() has run.
func (t *Toolchain) Variables() map[string]string {
	vars := map[string]string{}
	for lang, c := range t.Compilers {
		vars["CMAKE_"+lang+"_COMPILER"] = c.Path
		vars["CMAKE_"+lang+"_COMPILER_ID"] = c.ID
		if c.Version != "" {
			vars["CMAKE_"+lang+"_COMPILER_VERSION"] = c.Version
		}
	}
	if t.Archiver != "" {
		vars["CMAKE_AR"] = t.Archiver
	}
	if t.Linker != "" {
		vars["CMAKE_LINKER"] = t.Linker
	}
	vars["CMAKE_STATIC_LIBRARY_PREFIX"] = t.StaticPrefix
	vars["CMAKE_STATIC_LIBRARY_SUFFIX"] = t.StaticSuffix
	vars["CMAKE_SHARED_LIBRARY_PREFIX"] = t.SharedPrefix
	vars["CMAKE_SHARED_LIBRARY_SUFFIX"] = t.SharedSuffix
	vars["CMAKE_EXECUTABLE_SUFFIX"] = t.ExeSuffix
	if t.Kind() == MSVC {
		vars["MSVC"] = "1"
	}
	return vars
}

// SystemIncludes returns the directories the compiler needs on its command
// line because the environment does not supply them.
func (t *Toolchain) SystemIncludes() []string {
	if t.MSVC == nil {
		return nil
	}
	out := make([]string, 0, len(t.MSVC.Include))
	for _, d := range t.MSVC.Include {
		out = append(out, slash(d))
	}
	return out
}

// SystemLibDirs returns the library search directories for the same reason.
func (t *Toolchain) SystemLibDirs() []string {
	if t.MSVC == nil {
		return nil
	}
	out := make([]string, 0, len(t.MSVC.Lib))
	for _, d := range t.MSVC.Lib {
		out = append(out, slash(d))
	}
	return out
}

func slash(p string) string { return strings.ReplaceAll(p, `\`, "/") }

// LanguageOf reports which language compiles a source file, or "" if the
// extension is not one a compiler handles. Headers deliberately map to nothing:
// they are inputs to compilation, never units of it.
func LanguageOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".c":
		return "C"
	case ".cc", ".cpp", ".cxx", ".c++", ".cp", ".cppm", ".ixx":
		return "CXX"
	case ".m":
		return "OBJC"
	case ".mm":
		return "OBJCXX"
	case ".s", ".asm":
		return "ASM"
	case ".f", ".f90", ".f95", ".f03":
		return "Fortran"
	case ".cu":
		return "CUDA"
	default:
		return ""
	}
}

// ExtensionsFor is the inverse of [LanguageOf]: the file extensions that map to
// a language. The File API reports it so that an editor can decide which
// compiler a new file will use before the file has been added to any target.
func ExtensionsFor(language string) []string {
	switch language {
	case "C":
		return []string{"c"}
	case "CXX":
		return []string{"cc", "cpp", "cxx", "c++", "cp", "cppm", "ixx"}
	case "OBJC":
		return []string{"m"}
	case "OBJCXX":
		return []string{"mm"}
	case "ASM":
		return []string{"s", "asm"}
	case "Fortran":
		return []string{"f", "f90", "f95", "f03"}
	case "CUDA":
		return []string{"cu"}
	default:
		return nil
	}
}

// Flags renders the default compile and link flags a project expects to find.
//
// These are not decoration. CMAKE_<LANG>_FLAGS_RELEASE is where -O2 and
// -DNDEBUG live, so a build that ignores it produces an unoptimised Release
// with its assertions still firing -- and says "Release" while doing it. And
// CMAKE_<LANG>_FLAGS is the variable every project reaches for when it wants a
// warning level or a standard: `set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} -Wall")`
// is in more CMakeLists.txt files than target_compile_options is.
//
// They are seeded as cache entries rather than applied directly, because that
// is what makes them the project's to change: a CMakeLists.txt appends to them,
// a -D on the command line replaces them, and a build directory remembers what
// it was told.
func (t *Toolchain) Flags() map[string]string {
	out := map[string]string{}
	set := func(name, value string) { out[name] = value }

	if t.Kind() == MSVC {
		set("CMAKE_C_FLAGS", "/DWIN32 /D_WINDOWS")
		set("CMAKE_CXX_FLAGS", "/DWIN32 /D_WINDOWS /EHsc")
		for _, lang := range []string{"C", "CXX"} {
			set("CMAKE_"+lang+"_FLAGS_DEBUG", "/Ob0 /Od")
			set("CMAKE_"+lang+"_FLAGS_RELEASE", "/O2 /Ob2 /DNDEBUG")
			set("CMAKE_"+lang+"_FLAGS_MINSIZEREL", "/O1 /Ob1 /DNDEBUG")
			set("CMAKE_"+lang+"_FLAGS_RELWITHDEBINFO", "/O2 /Ob1 /DNDEBUG")
		}
		for _, kind := range []string{"EXE", "SHARED", "MODULE"} {
			set("CMAKE_"+kind+"_LINKER_FLAGS", "")
			set("CMAKE_"+kind+"_LINKER_FLAGS_DEBUG", "/debug /INCREMENTAL")
			set("CMAKE_"+kind+"_LINKER_FLAGS_RELEASE", "/INCREMENTAL:NO")
			set("CMAKE_"+kind+"_LINKER_FLAGS_MINSIZEREL", "/INCREMENTAL:NO")
			set("CMAKE_"+kind+"_LINKER_FLAGS_RELWITHDEBINFO", "/debug /INCREMENTAL")
		}
		set("CMAKE_STATIC_LINKER_FLAGS", "")
		// The runtime library is an ABI decision, not a preference: an object
		// built against the static runtime and one built against the DLL cannot
		// be linked together without the allocator in one freeing memory the
		// other allocated. CMake picks the DLL by default and so does this.
		set("CMAKE_MSVC_RUNTIME_LIBRARY", "MultiThreaded$<$<CONFIG:Debug>:Debug>DLL")
		set("CMAKE_MSVC_DEBUG_INFORMATION_FORMAT", "$<$<CONFIG:Debug,RelWithDebInfo>:ProgramDatabase>")
		return out
	}

	set("CMAKE_C_FLAGS", "")
	set("CMAKE_CXX_FLAGS", "")
	for _, lang := range []string{"C", "CXX"} {
		set("CMAKE_"+lang+"_FLAGS_DEBUG", "-g")
		set("CMAKE_"+lang+"_FLAGS_RELEASE", "-O3 -DNDEBUG")
		set("CMAKE_"+lang+"_FLAGS_MINSIZEREL", "-Os -DNDEBUG")
		set("CMAKE_"+lang+"_FLAGS_RELWITHDEBINFO", "-O2 -g -DNDEBUG")
	}
	for _, kind := range []string{"EXE", "SHARED", "MODULE", "STATIC"} {
		set("CMAKE_"+kind+"_LINKER_FLAGS", "")
		for _, cfg := range []string{"DEBUG", "RELEASE", "MINSIZEREL", "RELWITHDEBINFO"} {
			set("CMAKE_"+kind+"_LINKER_FLAGS_"+cfg, "")
		}
	}
	return out
}

// MSVCRuntimeFlag maps a CMAKE_MSVC_RUNTIME_LIBRARY value to its compiler
// option. An empty or unrecognised value selects nothing, which leaves the
// compiler's own default -- the same thing CMake does with an empty value.
func MSVCRuntimeFlag(value string) string {
	switch value {
	case "MultiThreaded":
		return "/MT"
	case "MultiThreadedDLL":
		return "/MD"
	case "MultiThreadedDebug":
		return "/MTd"
	case "MultiThreadedDebugDLL":
		return "/MDd"
	}
	return ""
}
