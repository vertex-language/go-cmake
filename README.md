# go-cmake

`cmake` is an implementation of CMake as a Go package, with no dependency on
anything but the standard library.

It reads a `CMakeLists.txt` tree, evaluates the CMake language, resolves the
target graph, writes a `build.ninja`, and drives that build to completion — all
in one process. Building a C or C++ project with it requires a compiler and
nothing else: no `cmake` binary, no `ninja` binary, and on Windows no developer
command prompt.

```go
import "github.com/vertex-language/go-cmake"

c, _ := cmake.New(cmake.Config{
    Source:    ".",
    Binary:    "_build",
    Generator: "Ninja",
    Vars:      map[string]string{"CMAKE_BUILD_TYPE": "Release"},
    Jobs:      8,
    FS:        cmake.RealFS(""),
    Runner:    run.OS(),
})
res, err := c.Build(ctx)
```

Every external effect is an injected field: the filesystem, the process runner,
the environment, and the output streams. That is what makes a dry run, a test
against an in-memory filesystem, or a fully in-process build possible.

---

- [Status](#status)
- [The three phases](#the-three-phases)
- [The pipeline](#the-pipeline)
- [The two seams](#the-two-seams)
- [Every effect is a field](#every-effect-is-a-field)
- [Toolchain discovery](#toolchain-discovery)
- [The command line](#the-command-line)
- [How it is tested](#how-it-is-tested)
- [What is not implemented](#what-is-not-implemented)
- [What this package does not own](#what-this-package-does-not-own)

---

## Install

As a library:

```console
$ go get github.com/vertex-language/go-cmake
```

As a program:

```console
$ go install github.com/vertex-language/go-cmake/cmd/cmake@latest
$ go install github.com/vertex-language/go-cmake/cmd/ctest@latest
```

---

## Status

This is a working implementation of the parts of CMake that a C or C++ project
actually uses, and an explicit non-implementation of the rest. The table says
where each package stands; the coverage figures are `go test -cover`.

| Package | State | Coverage |
|---|---|---|
| [`token`](token/) | complete | 93% |
| [`scanner`](scanner/) | complete | 79% |
| [`ast`](ast/) | complete | — |
| [`parser`](parser/) | complete | 76% |
| [`expr`](expr/) | complete | 64% |
| [`eval`](eval/) | 114 of 132 documented commands (97 in the command table, 17 control-flow constructs) | 55% |
| [`generate`](generate/) | usage-requirement closure, generator expressions, Ninja output | 51% |
| [`toolchain`](toolchain/) | GCC, Clang, MSVC discovery | — |
| [`ninja`](ninja/) | parser, scheduler, build log | 77% |
| [`build`](build/) | `--build` | — |
| [`ctest`](ctest/) | the test runner: reads the generated test files, runs them, reports | — |
| [`archive`](archive/) | tar and zip, for `-E tar`, `file(ARCHIVE_*)`, and fetched tarballs | — |
| [`regex`](regex/) | CMake's regular expressions, which are not Go's | — |
| [`run`](run/) | the one Command and Runner every phase uses | — |
| [`cli`](cli/) | configure, `-P`, `-E`, `--build` | 44% |
| [`cmake`](.) | the facade | 53% |

**What demonstrably works.** The end-to-end tests in
[`endtoend_test.go`](endtoend_test.go) compile, link, and *run* real programs
covering executables, static libraries, object libraries, subdirectory trees,
transitive usage requirements, `configure_file`, custom commands, generator
expressions, incremental rebuilds, and header-change detection.

---

## The three phases

CMake divides every build into three phases. Each is a distinct, independently
observable stage here.

| Phase | What happens |
|---|---|
| **Configure** | The CMake language is read and evaluated. Targets, sources, dependencies, compile flags, tests, and install rules are declared. The result is written to `CMakeCache.txt`, so a later run remembers what this one decided. Nothing is compiled. |
| **Generate** | The configured state is resolved into a build graph — usage requirements propagated, generator expressions evaluated, link order computed — and written as `build.ninja`. Nothing is compiled. |
| **Build** | The build graph is scheduled and executed. |

Installing and testing are further steps and deliberately not phases: generate writes a
`cmake_install.cmake` and a `CTestTestfile.cmake` into the build tree, and
`cmake --install` and `ctest` are those scripts read back. The prefix, the component, and the configuration
are questions that cannot be answered at generate time, so the script defers
them to the moment they can be. It also means a build tree carries a readable
list of exactly what it will install and where.

The boundary between configure and generate is a value you can hold. The
boundary between generate and build is a file on disk.

---

## The pipeline

Each stage is a sub-package depending only on the ones above it.

```
  CMakeLists.txt bytes
        |
   token|  token kinds and Pos: the vocabulary every later package shares
        |
 scanner|  bytes -> []Token: bracket syntax [[...]], quoted strings,
        |  unquoted args, bracket comments, line comments
        |
     ast|  node types: File, CommandInvocation, and the three Argument kinds
        |
  parser|  []Token -> *ast.File: the command-invocation grammar and a printer
        |  that reconstructs the source byte for byte
        |
    expr|  argument expansion: ${VAR}, nested ${${VAR}}, $ENV{}, $CACHE{},
        |  escapes, and the semicolon-list duality
        |
    eval|  variables, the cache, macros, functions, conditionals, loops,
        |  include, add_subdirectory, policies, and 93 built-in commands
        |        <- configure phase ends here, in an eval.State
        +
toolchain|  which compiler, which archiver, which flag convention, and on
        |  Windows where Visual Studio put them
        |
generate|  usage-requirement closure, link order, generator expressions,
        |  and the build.ninja
        |        <- generate phase ends here
        +
   ninja|  the build file parser, the scheduler that decides what not to run,
        |  and the build log that makes that decision correct
        |
   build|  cmake --build and cmake --install
        |        <- build phase: the only stage that changes anything
        v
     the files
```

[`run`](run/) sits beside all of them rather than in the chain: every phase that
starts a process goes through its one `Runner`, so intercepting compiler
invocations is a single substitution rather than four.

The CMake language they implement is specified in
[docs/grammar.md](docs/grammar.md) — productions rather than prose, because the
CMake documentation describes behaviour but publishes no grammar a parser can
be written against.

---

## The two seams

### Seam 1: after configure

The configured state is a value you can inspect before generating anything:

```go
res, _ := c.Configure(ctx)

fmt.Println(res.State.GetVar("CMAKE_PROJECT_NAME"))
fmt.Println(res.TargetNames)          // in declaration order
for _, name := range res.TargetNames {
    t, _ := res.State.Target(name)
    fmt.Println(t.Name, t.Type, t.Sources)
}
for _, rule := range res.State.InstallRules { ... }
for _, test := range res.State.Tests      { ... }
```

Stopping after configure is a supported use, not a debugging mode. A linter
wants `Configure`. A tool that only needs to know what a project declares is
done there, and needs no compiler at all.

### Seam 2: generator expressions

`$<TARGET_FILE:foo>`, `$<CONFIG:Release>`, `$<BUILD_INTERFACE:...>` are
deliberately opaque during configure. They cannot be answered then: `foo` may
not be declared yet, and its output path is a decision the *generator* makes.
So configure keeps them verbatim and [`generate`](generate/) resolves them
once the full graph and the toolchain are both known.

Variable references *inside* a generator expression are still expanded at
configure time — `$<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/inc>` reaches
the generator with the path already substituted. Only the `$<...>` structure
survives.

---

## Every effect is a field

The package holds no global state and opens nothing it was not given.

```go
type Config struct {
    Source    string            // the source directory (contains CMakeLists.txt)
    Binary    string            // the build directory
    Generator string            // "Ninja"
    Toolchain string            // -DCMAKE_TOOLCHAIN_FILE
    Preset    string            // --preset name
    Vars      map[string]string // -D assignments
    Env       []string          // defaults to os.Environ()
    Jobs      int               // --parallel
    Flags     Flags             // --log-level

    FS     FS
    Runner run.Runner
    Out    io.Writer
    Err    io.Writer
}
```

`FS` is the package's only contact with the disk. `Runner`, from
[`run`](run/), is how a caller takes over process execution without taking over
CMake. Every phase that starts a process — `execute_process` during configure,
compiler detection during generate, the compile and link lines during build,
and `cmake -E chdir` — goes through this one interface:

```go
// package run
type Runner interface {
    Run(ctx context.Context, cmd Command) (exitCode int, err error)
}

type Command struct {
    Argv   []string
    Line   string    // set when the command is a shell command line
    Dir    string
    Env    []string
    Stdin  io.Reader
    Stdout io.Writer
    Stderr io.Writer
}
```

`Line` exists for a specific reason. A build file's commands are shell syntax,
not argument vectors — they may contain `&&`, redirection, or quoting that only
a shell resolves. And on Windows there is no argument vector that survives the
trip: `cmd.exe` does not parse its arguments the way Go quotes them, so a
compiler path containing a space (which the default Visual Studio location has)
arrives mangled. A `Runner` that executes commands should prefer `Line`; one
that only inspects or logs them can read `Argv`.

`run.OS()` forks a subprocess. A `Runner` of your own can intercept compiler
invocations, cache object files, record inputs and outputs, or redirect output
to a structured log. A test `Runner` appends to a slice.

A non-zero exit is reported as a status, not an error; only a command that could
not be started at all returns one. That distinction is the whole reason the
interface has this shape: `execute_process` puts a status in `RESULT_VARIABLE`
and carries on, while a missing executable fails configure, and a compile step
that exits non-zero must fail the build rather than look like a broken runner.

---

## Toolchain discovery

CMake detects compilers inside `project()`, by writing a small program and
compiling it. That is a build-phase effect happening during configure, and it is
why a CMake configure needs a working compiler even to answer a question about a
project's structure.

This package keeps discovery separate, in [`toolchain`](toolchain/), and seeds
the results into the cache before evaluation begins. A `CMakeLists.txt` that
reads `CMAKE_C_COMPILER_ID` sees exactly what the generator will use.

Discovery looks at `CC` and `CXX` first, then the conventional names on `PATH`.
On Windows, when nothing is on `PATH` — which is the normal state, because
Visual Studio expects `vcvarsall.bat` to have been run first — it locates the
Visual C++ toolchain and the Windows SDK where they install, and composes the
include and library directories itself. Those directories are then emitted as
explicit flags in the generated build file rather than left to an environment
the build may not have.

---

## The command line

[`cli`](cli/) parses arguments into a `Config` and runs the phase asked for.
Every effect reaches it through `Env`, so the whole command line is testable
without a process.

```go
type Env struct {
    Args []string
    Dir  string
    Env  []string
    In   io.Reader
    Out  io.Writer
    Err  io.Writer
}

func Main(ctx context.Context, e Env) int
```

| Mode | |
|---|---|
| configure | `-S` `-B` `-G` `-C` `-D` `-U` `-T` `-A` `-N` `-L[A][H]` `-LR` `--preset` `--list-presets` `--toolchain` `--install-prefix` `--fresh` `--log-level` `-j` |
| build | `cmake --build <dir> [--target ...] [--config ...] [-j N] [--clean-first] [--verbose]` |
| install | `cmake --install <dir> [--prefix ...] [--component ...] [--config ...] [--strip]` |
| script | `cmake -P <script.cmake> [args...]` — the language with no project and no cache |
| tool | `cmake -E <command>` — the portable shell that generated build rules call |
| test | `ctest [--test-dir <dir>] [-R <r>] [-E <r>] [-L <r>] [-LE <r>] [-j N] [-N] [--output-on-failure] [--stop-on-failure]` |

`ctest` is a second binary, as it is for CMake, because it takes a build tree
rather than a source tree and its options mean different things: `-R` selects
tests there and is not an option for `cmake` at all.

Flags that change only diagnostics (`--trace`, `-Wdev`, `--warn-uninitialized`,
`--debug-find`, and friends) are accepted and ignored. Options CMake has that
this package does not — `--workflow`, `--open`, `--find-package` — fail by name
with a reason, which is a different thing from a typo and reads differently.

An unrecognised flag is still an error, because a misspelled flag that is
quietly discarded produces a build that is subtly not the one asked for. A test
holds this line: every option `cmake --help` publishes is run through this
command line, and none of them may come back as unknown.

---

## How it is tested

Two kinds of test, because they answer different questions.

**Differential tests** (`eval/differential_test.go`, `eval/differential2_test.go`,
`eval/project_test.go`) run the same CMake script through this implementation
and through the `cmake` binary installed on the host, and require the output to
match exactly. Reading the documentation tells you what CMake is supposed to do;
running it tells you what it does, and where the two differ, the binary is the
specification. Every one of these started as a guess that turned out to be
wrong — that `unset()` clears the cache entry (it does not), that `if(NOT NOT
V)` is a double negation (it is an error), that `" 5 "` is a number (it is not,
though `" 5 " LESS 10` is true), that a directory's compile definitions are
copied into its targets (they are not, unlike its include directories). If no
`cmake` is installed, these skip.

**Byte-for-byte tests** (`diagnostics_test.go`) are the differential tests with
the normalising taken away: the same script goes to both programs and the output
has to be equal, byte for byte, wrapping and blank lines included. A diagnostic
is the part of a build tool people actually read, and one that wraps at a
different column is a difference somebody has to reconcile by hand.

**Interoperation tests** (`export_test.go`, `archive/`) require the two
implementations to read each other's output. Real cmake configures against a
package this program installed, and this program configures against one real
cmake installed; both then run the program that came out. A generated file only
this program can read would pass every other test and still be useless.

**End-to-end tests** (`endtoend_test.go`) compile, link, and run real programs.
They are the only tests that can catch a build file that parses, schedules, and
produces a binary that does not work.

```console
$ go test ./...
```

---

## What is not implemented

Stated plainly, because a build system that quietly does nothing is worse than
one that says it cannot.

1. **Generators other than Ninja.** `Unix Makefiles`, `Visual Studio`, and
   `Xcode` are refused with a message, not approximated.
2. **`ExternalProject`.** `FetchContent` is implemented; `ExternalProject`,
   which drives a whole configure-build-install of its own at build time
   rather than at configure time, is not. `file(UPLOAD)` is refused: sending
   this machine's files to a remote host is not something building a project
   requires.
3. **CPack.** No packaging. The `ctest_*` script commands are absent too; they
   drive a dashboard submission rather than a test run.
4. **18 of 132 documented commands**, of which 13 are `ctest_*` -- they drive a
   dashboard submission rather than a build -- and the rest are CMake 4.x
   additions: `cmake_file_api` (the command; the File API itself is
   implemented), `cmake_pkg_config`, `cmake_instrumentation`,
   `cmake_diagnostic`, `discover_tests`.
5. **Multi-config generators.** One configuration per build directory.
6. **Precompiled headers, unity builds, LTO, and module (C++20) dependency
   scanning.**
7. **The regex engine's own chatter.** When a pattern will not compile, CMake
   writes two lines from its vendored C++ matcher — `RegularExpression::compile():
   Nested *?+.` and `Error in compile.` — before the command reports the failure.
   The failure itself is reported identically; those two lines are not. The
   reason is still there for anyone embedding the package: it is on the error.
8. **`.ninja_deps` binary compatibility.** The dependency log is written as
    text, and the build log's command field holds this implementation's hash
    rather than upstream ninja's. Sharing a build directory with real `ninja`
    costs one extra rebuild, never a stale object.

---

## What this package does not own

[`build`](build/) drives **commands**. A command is an opaque subprocess whose
inputs and outputs are not declared.

A build system integrating with this package may run **actions** — a command
with declared inputs, outputs, content hashes, and a cache key — which are
remotely cacheable and distributable. To bridge that gap, callers substitute a
`Runner` that recognises compiler invocations and turns them into cacheable
actions, delegating everything else.

Nothing about remote execution, content-addressed caching, distributed
scheduling, or compiler wrapper selection appears here. All such integrations
attach through the `Runner` interface.

---

## The package name

The module is `github.com/vertex-language/go-cmake`; the Go package name is
`cmake`. Importing it shadows no Go builtin.

---

## License

MIT. See [LICENSE](LICENSE).
