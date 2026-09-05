package toolchain

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Visual Studio does not put its compiler on PATH. It ships a batch file,
// vcvarsall.bat, whose entire job is to set three variables — PATH, INCLUDE and
// LIB — and developers are expected to have run it before invoking anything.
// A build tool that requires its user to have run a batch file first is a build
// tool that fails on a clean machine, so this file locates the toolchain and
// composes those variables itself.
//
// The layout is stable across releases:
//
//	<vs>/VC/Tools/MSVC/<version>/bin/Host<hostarch>/<targetarch>/cl.exe
//	<vs>/VC/Tools/MSVC/<version>/include
//	<vs>/VC/Tools/MSVC/<version>/lib/<targetarch>
//	<sdk>/Include/<sdkversion>/{ucrt,um,shared,winrt}
//	<sdk>/Lib/<sdkversion>/{ucrt,um}/<targetarch>

// MSVCInstall is a located Visual C++ toolchain and the Windows SDK it uses.
type MSVCInstall struct {
	ToolsDir   string   // <vs>/VC/Tools/MSVC/<version>
	BinDir     string   // the directory holding cl.exe, link.exe and lib.exe
	SDKRoot    string   // <sdk>
	SDKVersion string   // e.g. 10.0.26100.0
	Include    []string // directories for INCLUDE
	Lib        []string // directories for LIB
	Arch       string   // the target architecture: x64, x86, arm64
}

// vsSearchRoots lists where Visual Studio installs itself.
func vsSearchRoots() []string {
	var roots []string
	for _, base := range []string{
		os.Getenv("ProgramFiles"),
		os.Getenv("ProgramFiles(x86)"),
		`C:\Program Files`,
		`C:\Program Files (x86)`,
	} {
		if base == "" {
			continue
		}
		roots = append(roots, filepath.Join(base, "Microsoft Visual Studio"))
	}
	return roots
}

// FindMSVC locates the newest Visual C++ toolchain on this machine, or returns
// nil when there is none.
func FindMSVC(arch string) *MSVCInstall {
	if runtime.GOOS != "windows" {
		return nil
	}
	if arch == "" {
		arch = hostArch()
	}

	toolsDir := newestMatch(vsSearchRoots(), []string{"*", "*", "VC", "Tools", "MSVC", "*"})
	if toolsDir == "" {
		return nil
	}

	binDir := filepath.Join(toolsDir, "bin", "Host"+hostArch(), arch)
	if _, err := os.Stat(filepath.Join(binDir, "cl.exe")); err != nil {
		// A host/target pair that was not installed; fall back to the host
		// compiler targeting its own architecture, which always exists.
		binDir = filepath.Join(toolsDir, "bin", "Host"+hostArch(), hostArch())
		if _, err := os.Stat(filepath.Join(binDir, "cl.exe")); err != nil {
			return nil
		}
		arch = hostArch()
	}

	m := &MSVCInstall{ToolsDir: toolsDir, BinDir: binDir, Arch: arch}
	m.Include = append(m.Include, filepath.Join(toolsDir, "include"))
	m.Lib = append(m.Lib, filepath.Join(toolsDir, "lib", arch))

	// The Windows SDK supplies the C runtime headers and the system libraries.
	// Without it cl.exe cannot compile a program that includes <stdio.h>.
	if root, version := findWindowsSDK(); root != "" {
		m.SDKRoot, m.SDKVersion = root, version
		for _, part := range []string{"ucrt", "um", "shared", "winrt", "cppwinrt"} {
			dir := filepath.Join(root, "Include", version, part)
			if _, err := os.Stat(dir); err == nil {
				m.Include = append(m.Include, dir)
			}
		}
		for _, part := range []string{"ucrt", "um"} {
			dir := filepath.Join(root, "Lib", version, part, arch)
			if _, err := os.Stat(dir); err == nil {
				m.Lib = append(m.Lib, dir)
			}
		}
	}
	return m
}

// findWindowsSDK locates the newest installed Windows 10/11 SDK.
func findWindowsSDK() (root, version string) {
	for _, base := range []string{
		os.Getenv("ProgramFiles(x86)"),
		os.Getenv("ProgramFiles"),
		`C:\Program Files (x86)`,
		`C:\Program Files`,
	} {
		if base == "" {
			continue
		}
		kit := filepath.Join(base, "Windows Kits", "10")
		versions, err := filepath.Glob(filepath.Join(kit, "Include", "*"))
		if err != nil || len(versions) == 0 {
			continue
		}
		// A version directory only counts if it actually holds the CRT headers;
		// the SDK installer leaves empty directories behind.
		var usable []string
		for _, v := range versions {
			if _, err := os.Stat(filepath.Join(v, "ucrt", "stdio.h")); err == nil {
				usable = append(usable, v)
			}
		}
		if len(usable) == 0 {
			continue
		}
		sort.Sort(byVersion(usable))
		return kit, filepath.Base(usable[len(usable)-1])
	}
	return "", ""
}

// newestMatch expands a glob under each root and returns the highest-versioned
// directory that exists.
func newestMatch(roots []string, parts []string) string {
	var found []string
	for _, root := range roots {
		pattern := filepath.Join(append([]string{root}, parts...)...)
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil && fi.IsDir() {
				found = append(found, m)
			}
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.Sort(byVersion(found))
	return found[len(found)-1]
}

// byVersion orders paths by the dotted numbers in their last component, so
// that 14.44.35207 sorts after 14.9.1 rather than before it.
type byVersion []string

func (v byVersion) Len() int      { return len(v) }
func (v byVersion) Swap(i, j int) { v[i], v[j] = v[j], v[i] }
func (v byVersion) Less(i, j int) bool {
	return compareVersionStrings(filepath.Base(v[i]), filepath.Base(v[j])) < 0
}

func compareVersionStrings(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		x, y := 0, 0
		if i < len(as) {
			x = leadingInt(as[i])
		}
		if i < len(bs) {
			y = leadingInt(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	if a == b {
		return 0
	}
	if a < b {
		return -1
	}
	return 1
}

func leadingInt(s string) int {
	n := 0
	for i := 0; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func hostArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "x86"
	case "arm64":
		return "arm64"
	default:
		return "x64"
	}
}

// Environment returns the INCLUDE and LIB assignments the compiler needs, in
// the KEY=VALUE form a process environment takes.
func (m *MSVCInstall) Environment() []string {
	return []string{
		"INCLUDE=" + strings.Join(m.Include, ";"),
		"LIB=" + strings.Join(m.Lib, ";"),
	}
}

// tool returns the path to one of the toolchain's executables.
func (m *MSVCInstall) tool(name string) string {
	p := filepath.Join(m.BinDir, name+".exe")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return slash(p)
}
