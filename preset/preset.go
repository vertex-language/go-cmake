// Package preset implements CMakePresets.json and CMakeUserPresets.json parsing.
//
// It supports CMakePresets.json schema versions 1–9 as shipped with CMake 4.4.3.
//
// Usage:
//
//	ps, err := preset.Load(".")
//	cfg, err := ps.Resolve("release", preset.Configure)
package preset

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Kind indicates which preset type to resolve.
type Kind int

const (
	Configure Kind = iota
	Build
	Test
	Package
	Workflow
)

// ----------------------------------------------------------------------------
// JSON schema types

// File is the parsed content of one CMakePresets.json or CMakeUserPresets.json.
type File struct {
	Schema               string            `json:"$schema"`
	Version              int               `json:"version"`
	CMakeMinimumRequired *CMakeVersion     `json:"cmakeMinimumRequired,omitempty"`
	Include              []string          `json:"include,omitempty"`
	ConfigurePresets     []ConfigurePreset `json:"configurePresets,omitempty"`
	BuildPresets         []BuildPreset     `json:"buildPresets,omitempty"`
	TestPresets          []TestPreset      `json:"testPresets,omitempty"`
	PackagePresets       []PackagePreset   `json:"packagePresets,omitempty"`
	WorkflowPresets      []WorkflowPreset  `json:"workflowPresets,omitempty"`
}

// CMakeVersion is a version triple with optional tweak.
type CMakeVersion struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
	Patch int `json:"patch"`
}

// Condition evaluates whether a preset is active.
type Condition struct {
	Type       string      `json:"type"` // "const", "equals", "notEquals", "inList", "notInList", "matches", "notMatches", "allOf", "anyOf", "not"
	Value      *bool       `json:"value,omitempty"`
	LHS        string      `json:"lhs,omitempty"`
	RHS        string      `json:"rhs,omitempty"`
	String     string      `json:"string,omitempty"`
	Regex      string      `json:"regex,omitempty"`
	List       []string    `json:"list,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
	Condition  *Condition  `json:"condition,omitempty"`
}

// ConfigurePreset holds a configure preset.
type ConfigurePreset struct {
	Name           string               `json:"name"`
	DisplayName    string               `json:"displayName,omitempty"`
	Description    string               `json:"description,omitempty"`
	Hidden         bool                 `json:"hidden,omitempty"`
	Inherits       StringOrList         `json:"inherits,omitempty"`
	Condition      *Condition           `json:"condition,omitempty"`
	Generator      string               `json:"generator,omitempty"`
	Architecture   *GeneratorConfig     `json:"architecture,omitempty"`
	Toolset        *GeneratorConfig     `json:"toolset,omitempty"`
	BinaryDir      string               `json:"binaryDir,omitempty"`
	InstallDir     string               `json:"installDir,omitempty"`
	ToolchainFile  string               `json:"toolchainFile,omitempty"`
	CacheVariables map[string]*CacheVar `json:"cacheVariables,omitempty"`
	Environment    map[string]*string   `json:"environment,omitempty"`
	WarnDev        *bool                `json:"warnings,omitempty"`
	ErrorDev       *bool                `json:"errors,omitempty"`
	Debug          map[string]bool      `json:"debug,omitempty"`
}

// BuildPreset holds a build preset.
type BuildPreset struct {
	Name                        string       `json:"name"`
	DisplayName                 string       `json:"displayName,omitempty"`
	Description                 string       `json:"description,omitempty"`
	Hidden                      bool         `json:"hidden,omitempty"`
	Inherits                    StringOrList `json:"inherits,omitempty"`
	Condition                   *Condition   `json:"condition,omitempty"`
	ConfigurePreset             string       `json:"configurePreset,omitempty"`
	InheritConfigureEnvironment *bool        `json:"inheritConfigureEnvironment,omitempty"`
	Jobs                        *int         `json:"jobs,omitempty"`
	Targets                     StringOrList `json:"targets,omitempty"`
	Configuration               string       `json:"configuration,omitempty"`
	CleanFirst                  *bool        `json:"cleanFirst,omitempty"`
	Verbose                     *bool        `json:"verbose,omitempty"`
}

// TestPreset holds a test preset.
type TestPreset struct {
	Name            string         `json:"name"`
	DisplayName     string         `json:"displayName,omitempty"`
	Description     string         `json:"description,omitempty"`
	Hidden          bool           `json:"hidden,omitempty"`
	Inherits        StringOrList   `json:"inherits,omitempty"`
	Condition       *Condition     `json:"condition,omitempty"`
	ConfigurePreset string         `json:"configurePreset,omitempty"`
	Configuration   string         `json:"configuration,omitempty"`
	Output          *TestOutput    `json:"output,omitempty"`
	Filter          *TestFilter    `json:"filter,omitempty"`
	Execution       *TestExecution `json:"execution,omitempty"`
}

// PackagePreset holds a package (CPack) preset.
type PackagePreset struct {
	Name            string            `json:"name"`
	DisplayName     string            `json:"displayName,omitempty"`
	Hidden          bool              `json:"hidden,omitempty"`
	Inherits        StringOrList      `json:"inherits,omitempty"`
	Condition       *Condition        `json:"condition,omitempty"`
	ConfigurePreset string            `json:"configurePreset,omitempty"`
	Generators      []string          `json:"generators,omitempty"`
	Configurations  []string          `json:"configurations,omitempty"`
	Variables       map[string]string `json:"variables,omitempty"`
	PackageName     string            `json:"packageName,omitempty"`
	PackageVersion  string            `json:"packageVersion,omitempty"`
}

// WorkflowPreset sequences configure, build, test, and package steps.
type WorkflowPreset struct {
	Name        string         `json:"name"`
	DisplayName string         `json:"displayName,omitempty"`
	Description string         `json:"description,omitempty"`
	Steps       []WorkflowStep `json:"steps,omitempty"`
}

// WorkflowStep is one step in a workflow preset.
type WorkflowStep struct {
	Type string `json:"type"` // "configure", "build", "test", "package"
	Name string `json:"name"`
}

// GeneratorConfig specifies a generator toolset or architecture with optional strategy.
type GeneratorConfig struct {
	Value    string `json:"value,omitempty"`
	Strategy string `json:"strategy,omitempty"` // "set" or "external"
}

// CacheVar is a CMake cache variable with optional type.
type CacheVar struct {
	Type  string      `json:"type,omitempty"`
	Value interface{} `json:"value"` // string or bool
}

// StringOrList is a JSON field that can be either a string or []string.
type StringOrList []string

func (s *StringOrList) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		*s = StringOrList{str}
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*s = list
		return nil
	}
	return fmt.Errorf("preset: cannot unmarshal %s as string or []string", data)
}

// TestOutput controls ctest output options.
type TestOutput struct {
	ShortProgress   *bool  `json:"shortProgress,omitempty"`
	Verbosity       string `json:"verbosity,omitempty"`
	Debug           *bool  `json:"debug,omitempty"`
	OutputOnFailure *bool  `json:"outputOnFailure,omitempty"`
	Quiet           *bool  `json:"quiet,omitempty"`
	OutputLogFile   string `json:"outputLogFile,omitempty"`
	LabelSummary    *bool  `json:"labelSummary,omitempty"`
}

// TestFilter controls ctest -R/-E/-L/-LE options.
type TestFilter struct {
	Include *TestFilterInclude `json:"include,omitempty"`
	Exclude *TestFilterExclude `json:"exclude,omitempty"`
}

// TestFilterInclude specifies which tests to include.
type TestFilterInclude struct {
	Name  string           `json:"name,omitempty"`
	Label string           `json:"label,omitempty"`
	Index *TestFilterIndex `json:"index,omitempty"`
}

// TestFilterExclude specifies which tests to exclude.
type TestFilterExclude struct {
	Name  string `json:"name,omitempty"`
	Label string `json:"label,omitempty"`
}

// TestFilterIndex specifies a range of tests by number.
type TestFilterIndex struct {
	Start  *int `json:"start,omitempty"`
	End    *int `json:"end,omitempty"`
	Stride *int `json:"stride,omitempty"`
}

// TestExecution controls ctest execution options.
type TestExecution struct {
	StopOnFailure    *bool       `json:"stopOnFailure,omitempty"`
	EnableFailover   *bool       `json:"enableFailover,omitempty"`
	Jobs             *int        `json:"jobs,omitempty"`
	ResourceSpecFile string      `json:"resourceSpecFile,omitempty"`
	Timeout          *int        `json:"timeout,omitempty"`
	Repeat           *TestRepeat `json:"repeat,omitempty"`
}

// TestRepeat controls test repeat options.
type TestRepeat struct {
	Mode  string `json:"mode"` // "until-fail", "until-pass", "after-timeout"
	Count int    `json:"count"`
}

// ----------------------------------------------------------------------------
// PresetFile collection

// PresetFile holds all presets loaded from a source directory.
type PresetFile struct {
	dir   string  // source directory
	files []*File // project file first, user file (if any) appended
}

// Load reads CMakePresets.json and (if present) CMakeUserPresets.json from dir.
func Load(dir string) (*PresetFile, error) {
	pf := &PresetFile{dir: dir}

	projectFile := filepath.Join(dir, "CMakePresets.json")
	userFile := filepath.Join(dir, "CMakeUserPresets.json")

	f, err := loadFile(projectFile, dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("preset: %w", err)
	}
	if f != nil {
		pf.files = append(pf.files, f)
	}

	u, err := loadFile(userFile, dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("preset: %w", err)
	}
	if u != nil {
		pf.files = append(pf.files, u)
	}

	return pf, nil
}

// loadFile reads and parses one preset file, following its include directives.
func loadFile(path, baseDir string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// Process includes.
	for _, inc := range f.Include {
		if !filepath.IsAbs(inc) {
			inc = filepath.Join(baseDir, inc)
		}
		included, err := loadFile(inc, filepath.Dir(inc))
		if err != nil {
			return nil, fmt.Errorf("%s: include %s: %w", path, inc, err)
		}
		// Merge included presets into f.
		f.ConfigurePresets = append(included.ConfigurePresets, f.ConfigurePresets...)
		f.BuildPresets = append(included.BuildPresets, f.BuildPresets...)
		f.TestPresets = append(included.TestPresets, f.TestPresets...)
		f.PackagePresets = append(included.PackagePresets, f.PackagePresets...)
	}
	return &f, nil
}

// ----------------------------------------------------------------------------
// Resolution

// ResolvedConfigure holds the resolved configure preset settings.
type ResolvedConfigure struct {
	Name          string
	Generator     string
	BinaryDir     string
	InstallDir    string
	ToolchainFile string
	CacheVars     map[string]string // name -> value (bools converted to "ON"/"OFF")
	Environment   map[string]string // inherited env overrides
}

// Resolve resolves a named configure preset, following inherits chains.
// It returns the merged settings after macro expansion.
func (pf *PresetFile) Resolve(name string) (*ResolvedConfigure, error) {
	// Build index of all configure presets across all files.
	index := make(map[string]*ConfigurePreset)
	for _, f := range pf.files {
		for i := range f.ConfigurePresets {
			p := &f.ConfigurePresets[i]
			index[p.Name] = p
		}
	}

	p, ok := index[name]
	if !ok {
		return nil, fmt.Errorf("preset %q not found", name)
	}

	// Collect the inheritance chain (depth-first, base presets first).
	chain, err := resolveChain(name, index, nil)
	if err != nil {
		return nil, err
	}

	// Merge from base to derived.
	merged := &ResolvedConfigure{
		Name:        name,
		CacheVars:   make(map[string]string),
		Environment: make(map[string]string),
	}
	_ = p
	for _, c := range chain {
		if c.Generator != "" {
			merged.Generator = expandMacros(c.Generator, pf.dir, name)
		}
		if c.BinaryDir != "" {
			merged.BinaryDir = expandMacros(c.BinaryDir, pf.dir, name)
		}
		if c.InstallDir != "" {
			merged.InstallDir = expandMacros(c.InstallDir, pf.dir, name)
		}
		if c.ToolchainFile != "" {
			merged.ToolchainFile = expandMacros(c.ToolchainFile, pf.dir, name)
		}
		for k, v := range c.CacheVariables {
			if v == nil {
				delete(merged.CacheVars, k)
				continue
			}
			switch val := v.Value.(type) {
			case string:
				merged.CacheVars[k] = expandMacros(val, pf.dir, name)
			case bool:
				if val {
					merged.CacheVars[k] = "ON"
				} else {
					merged.CacheVars[k] = "OFF"
				}
			case float64:
				merged.CacheVars[k] = fmt.Sprintf("%g", val)
			}
		}
		for k, v := range c.Environment {
			if v == nil {
				delete(merged.Environment, k)
			} else {
				merged.Environment[k] = expandMacros(*v, pf.dir, name)
			}
		}
	}

	return merged, nil
}

// resolveChain returns the inheritance chain for a preset in base-first order.
// Detects cycles.
func resolveChain(name string, index map[string]*ConfigurePreset, visited []string) ([]*ConfigurePreset, error) {
	for _, v := range visited {
		if v == name {
			return nil, fmt.Errorf("preset inherits cycle: %s", strings.Join(append(visited, name), " -> "))
		}
	}
	p, ok := index[name]
	if !ok {
		return nil, fmt.Errorf("preset %q not found (referenced in inherits)", name)
	}
	visited = append(visited, name)

	var chain []*ConfigurePreset
	for _, base := range p.Inherits {
		baseChain, err := resolveChain(base, index, visited)
		if err != nil {
			return nil, err
		}
		chain = append(chain, baseChain...)
	}
	chain = append(chain, p)
	return chain, nil
}

// expandMacros expands preset macro references in s.
// Supported macros:
//
//	${sourceDir}        — the source directory
//	${sourceParentDir}  — parent of the source directory
//	${sourceDirName}    — last component of the source directory
//	${presetName}       — the preset name
//	${hostSystemName}   — GOOS-derived host system name
//	${fileDir}          — directory of the preset file (same as sourceDir for now)
//	$env{VAR}           — environment variable
//	$penv{VAR}          — parent process environment variable
func expandMacros(s, sourceDir, presetName string) string {
	hostSystem := goosToHostSystem()

	macros := map[string]string{
		"${sourceDir}":       sourceDir,
		"${sourceParentDir}": filepath.Dir(sourceDir),
		"${sourceDirName}":   filepath.Base(sourceDir),
		"${presetName}":      presetName,
		"${hostSystemName}":  hostSystem,
		"${fileDir}":         sourceDir,
		"${dollar}":          "$",
	}
	result := s
	for k, v := range macros {
		result = strings.ReplaceAll(result, k, v)
	}
	// Expand $env{VAR} and $penv{VAR}.
	result = expandEnvMacro(result, "$env{", false)
	result = expandEnvMacro(result, "$penv{", true)
	return result
}

func expandEnvMacro(s, prefix string, useOS bool) string {
	for {
		start := strings.Index(s, prefix)
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], "}")
		if end < 0 {
			break
		}
		end += start
		varName := s[start+len(prefix) : end]
		val := os.Getenv(varName)
		s = s[:start] + val + s[end+1:]
	}
	return s
}

func goosToHostSystem() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows"
	case "darwin":
		return "Darwin"
	case "linux":
		return "Linux"
	default:
		return runtime.GOOS
	}
}

// ConfigurePresetNames returns all non-hidden configure preset names.
func (pf *PresetFile) ConfigurePresetNames() []string {
	seen := make(map[string]bool)
	var names []string
	for _, f := range pf.files {
		for _, p := range f.ConfigurePresets {
			if !p.Hidden && !seen[p.Name] {
				seen[p.Name] = true
				names = append(names, p.Name)
			}
		}
	}
	return names
}
