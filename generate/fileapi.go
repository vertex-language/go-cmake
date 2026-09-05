package generate

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/vertex-language/go-cmake/eval"
	"github.com/vertex-language/go-cmake/toolchain"
)

// The File API is how an IDE understands a project.
//
// VS Code's CMake Tools, CLion, and Qt Creator do not parse CMakeLists.txt.
// They write a query file into the build tree, run cmake, and read back JSON
// describing every target, its sources, and the flags each is compiled with.
// Without it an editor can list files and run a build but cannot tell which
// target a file belongs to, which is most of what it is for.
//
// The protocol is deliberately one-way and file-based: the client leaves a
// question in .cmake/api/v1/query and finds the answer in
// .cmake/api/v1/reply after the next configure. Nothing is negotiated, so a
// client that asks for an object kind this implementation does not produce
// simply finds it missing from the index rather than failing.

const (
	apiQueryDir = ".cmake/api/v1/query"
	apiReplyDir = ".cmake/api/v1/reply"

	// codemodelMinor is the version of the codemodel object this produces. A
	// client checks the major version and tolerates a minor it does not know,
	// so claiming less than is emitted is safe and claiming more is not.
	codemodelMajor, codemodelMinor = 2, 6
)

// FileAPI renders the reply for a configured project.
type FileAPI struct {
	Graph        *Graph
	Toolchain    *toolchain.Toolchain
	SourceDir    string
	BinaryDir    string
	Generator    string
	CMakeCommand string
	Version      string
}

// Request is one object a client asked for.
type Request struct {
	Kind    string `json:"kind"`
	Version any    `json:"version,omitempty"`
}

// clientQuery is the shape of a client's query.json.
type clientQuery struct {
	Requests []Request `json:"requests"`
	Client   any       `json:"client,omitempty"`
}

// File is one file of the reply.
type File struct {
	Path    string
	Content []byte
}

// QueryDir is where a client leaves its request, relative to the build tree.
func QueryDir() string { return apiQueryDir }

// ReplyDir is where the answers go, relative to the build tree.
func ReplyDir() string { return apiReplyDir }

// Reply renders every file of the answer.
//
// queries maps a client name to what it asked for; the empty name is the
// shared query directory, where a client with no name of its own leaves
// stateless requests. A caller that found no queries at all should not call
// this: writing a reply nobody asked for leaves a stale index that the next
// client would read as current.
func (f *FileAPI) Reply(queries map[string][]Request) ([]File, error) {
	var files []File

	// Each object is generated once and referenced by every client that asked
	// for it, which is why the index carries a shared object list and each
	// client's section only names entries from it.
	produced := map[string]indexObject{}
	add := func(kind string, major, minor int, content any) (indexObject, error) {
		if obj, ok := produced[kind]; ok {
			return obj, nil
		}
		data, err := marshal(content)
		if err != nil {
			return indexObject{}, err
		}
		name := fmt.Sprintf("%s-v%d-%s.json", kind, major, contentHash(data))
		files = append(files, File{Path: path.Join(apiReplyDir, name), Content: data})
		obj := indexObject{
			Kind:     kind,
			Version:  objectVersion{Major: major, Minor: minor},
			JSONFile: name,
		}
		produced[kind] = obj
		return obj, nil
	}

	answers := map[string][]indexObject{}
	for client, requests := range queries {
		for _, req := range requests {
			var obj indexObject
			var err error
			switch req.Kind {
			case "codemodel":
				var extra []File
				var model codemodel
				model, extra = f.codemodel()
				files = append(files, extra...)
				obj, err = add("codemodel", codemodelMajor, codemodelMinor, model)
			case "cache":
				obj, err = add("cache", 2, 0, f.cache())
			case "cmakeFiles":
				obj, err = add("cmakeFiles", 1, 1, f.cmakeFiles())
			case "toolchains":
				obj, err = add("toolchains", 1, 1, f.toolchains())
			default:
				// An object kind this implementation does not produce is left
				// out rather than faked. A client sees it missing and can say
				// so; a client handed an empty object would report a project
				// with no targets.
				continue
			}
			if err != nil {
				return nil, err
			}
			answers[client] = append(answers[client], obj)
		}
	}

	index, err := marshal(f.index(queries, answers, produced))
	if err != nil {
		return nil, err
	}
	name := "index-" + time.Now().UTC().Format("2006-01-02T15-04-05-0000") + ".json"
	files = append(files, File{Path: path.Join(apiReplyDir, name), Content: index})
	return files, nil
}

// ----------------------------------------------------------------------------
// The index

type objectVersion struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
}

type indexObject struct {
	Kind     string        `json:"kind"`
	Version  objectVersion `json:"version"`
	JSONFile string        `json:"jsonFile"`
}

func (f *FileAPI) index(queries map[string][]Request, answers map[string][]indexObject, produced map[string]indexObject) map[string]any {
	major, minor, patch := splitVersion(f.Version)

	var objects []indexObject
	for _, kind := range sortedKeysOf(produced) {
		objects = append(objects, produced[kind])
	}

	reply := map[string]any{}
	for client, requests := range queries {
		responses := answers[client]
		if responses == nil {
			responses = []indexObject{}
		}
		if client == "" {
			// The shared query directory answers by object name rather than
			// through a query.json, because there was no file to answer.
			for _, obj := range responses {
				reply[fmt.Sprintf("%s-v%d", obj.Kind, obj.Version.Major)] = obj
			}
			continue
		}
		reply[client] = map[string]any{
			"query.json": map[string]any{
				"requests":  requests,
				"responses": responses,
			},
		}
	}

	return map[string]any{
		"cmake": map[string]any{
			"version": map[string]any{
				"major": major, "minor": minor, "patch": patch,
				"string": f.Version, "suffix": "", "isDirty": false,
			},
			"paths": map[string]any{
				"cmake": f.CMakeCommand,
				"ctest": siblingTool(f.CMakeCommand, "ctest"),
				"cpack": siblingTool(f.CMakeCommand, "cpack"),
				"root":  path.Dir(f.CMakeCommand),
			},
			"generator": map[string]any{
				"name":        f.Generator,
				"multiConfig": false,
			},
		},
		"objects": objects,
		"reply":   reply,
	}
}

// siblingTool names a companion program next to this one, which is where a
// client looks for ctest rather than searching PATH.
func siblingTool(cmakePath, name string) string {
	dir := path.Dir(cmakePath)
	suffix := ""
	if strings.HasSuffix(strings.ToLower(cmakePath), ".exe") {
		suffix = ".exe"
	}
	return path.Join(dir, name+suffix)
}

// ----------------------------------------------------------------------------
// codemodel

type codemodel struct {
	Kind           string                `json:"kind"`
	Version        objectVersion         `json:"version"`
	Paths          map[string]string     `json:"paths"`
	Configurations []codemodelConfigJSON `json:"configurations"`
}

type codemodelConfigJSON struct {
	Name string `json:"name"`

	// AbstractTargets is always present, empty or not: a client that indexes
	// into it rather than checking for it would fault on a missing key.
	AbstractTargets []any `json:"abstractTargets"`

	Directories []directoryRef  `json:"directories"`
	Projects    []projectRef    `json:"projects"`
	Targets     []targetRefJSON `json:"targets"`
}

type directoryRef struct {
	Source         string `json:"source"`
	Build          string `json:"build"`
	ProjectIndex   int    `json:"projectIndex"`
	TargetIndexes  []int  `json:"targetIndexes,omitempty"`
	ParentIndex    *int   `json:"parentIndex,omitempty"`
	ChildIndexes   []int  `json:"childIndexes,omitempty"`
	HasInstallRule bool   `json:"hasInstallRule,omitempty"`
	JSONFile       string `json:"jsonFile"`
}

type projectRef struct {
	Name             string `json:"name"`
	DirectoryIndexes []int  `json:"directoryIndexes"`
	TargetIndexes    []int  `json:"targetIndexes,omitempty"`
}

type targetRefJSON struct {
	Name           string `json:"name"`
	ID             string `json:"id"`
	DirectoryIndex int    `json:"directoryIndex"`
	ProjectIndex   int    `json:"projectIndex"`
	JSONFile       string `json:"jsonFile"`
}

// codemodel builds the object and the per-target and per-directory files it
// points at.
func (f *FileAPI) codemodel() (codemodel, []File) {
	state := f.Graph.State
	config := state.GetVar("CMAKE_BUILD_TYPE")

	var extra []File
	dirIndex := map[string]int{}
	var dirs []directoryRef
	for i, d := range state.AllDirs {
		dirIndex[d.Binary] = i
	}

	// Targets first: a directory names the targets it holds.
	targetsOfDir := map[int][]int{}
	var targets []targetRefJSON
	for _, name := range f.Graph.Order {
		r, ok := f.Graph.Targets[name]
		if !ok {
			continue
		}
		di := dirIndex[r.Target.BinaryDir]
		data, err := marshal(f.target(r, config))
		if err != nil {
			continue
		}
		file := fmt.Sprintf("target-%s-%s-%s.json", name, configOrDefault(config), contentHash(data))
		extra = append(extra, File{Path: path.Join(apiReplyDir, file), Content: data})

		targetsOfDir[di] = append(targetsOfDir[di], len(targets))
		targets = append(targets, targetRefJSON{
			Name:           name,
			ID:             targetID(name, r.Target.SourceDir),
			DirectoryIndex: di,
			ProjectIndex:   0,
			JSONFile:       file,
		})
	}

	var allDirIndexes []int
	for i, d := range state.AllDirs {
		data, err := marshal(f.directory(d, config, targetsOfDir[i]))
		if err != nil {
			continue
		}
		rel := relativeTo(f.BinaryDir, d.Binary)
		if rel == "" {
			rel = "."
		}
		file := fmt.Sprintf("directory-%s-%s-%s.json", strings.ReplaceAll(rel, "/", "-"),
			configOrDefault(config), contentHash(data))
		extra = append(extra, File{Path: path.Join(apiReplyDir, file), Content: data})

		ref := directoryRef{
			Source:        relOrDot(f.SourceDir, d.Source),
			Build:         relOrDot(f.BinaryDir, d.Binary),
			ProjectIndex:  0,
			TargetIndexes: targetsOfDir[i],
			JSONFile:      file,
		}
		if d.Parent != nil {
			if pi, ok := dirIndex[d.Parent.Binary]; ok {
				p := pi
				ref.ParentIndex = &p
			}
		}
		for _, child := range d.Children {
			if ci, ok := dirIndex[child.Binary]; ok {
				ref.ChildIndexes = append(ref.ChildIndexes, ci)
			}
		}
		ref.HasInstallRule = f.dirHasInstallRule(d)
		dirs = append(dirs, ref)
		allDirIndexes = append(allDirIndexes, i)
	}

	allTargets := make([]int, len(targets))
	for i := range targets {
		allTargets[i] = i
	}

	projectName := state.GetVar("CMAKE_PROJECT_NAME")
	if projectName == "" {
		projectName = "Project"
	}

	return codemodel{
		Kind:    "codemodel",
		Version: objectVersion{Major: codemodelMajor, Minor: codemodelMinor},
		Paths: map[string]string{
			"source": f.SourceDir,
			"build":  f.BinaryDir,
		},
		Configurations: []codemodelConfigJSON{{
			Name:            config,
			AbstractTargets: []any{},
			Directories:     dirs,
			Projects: []projectRef{{
				Name:             projectName,
				DirectoryIndexes: allDirIndexes,
				TargetIndexes:    allTargets,
			}},
			Targets: targets,
		}},
	}, extra
}

func (f *FileAPI) dirHasInstallRule(d *eval.Directory) bool {
	for _, rule := range f.Graph.State.InstallRules {
		if rule.SourceDir == d.Source {
			return true
		}
	}
	return false
}

// targetID is the stable identifier a client uses to match a target across
// configures. CMake derives it from the directory, which is what makes two
// targets of the same name in different directories distinguishable.
func targetID(name, sourceDir string) string {
	h := sha1.Sum([]byte(sourceDir))
	return name + "::@" + hex.EncodeToString(h[:])[:20]
}

func configOrDefault(config string) string {
	if config == "" {
		return "noconfig"
	}
	return config
}

func relOrDot(base, target string) string {
	rel := relativeTo(base, target)
	if rel == "" {
		return "."
	}
	return rel
}

// ----------------------------------------------------------------------------
// target objects

func (f *FileAPI) target(r *Resolved, config string) map[string]any {
	t := r.Target
	n := &Ninja{Graph: f.Graph, Toolchain: f.Toolchain, SourceDir: f.SourceDir, BinaryDir: f.BinaryDir}

	// Sources and the compile groups that describe how each is built. Every
	// source of one language shares a group, which is the compression that
	// makes this file small for a target with a thousand files.
	var sources []map[string]any
	groups := map[string][]int{}
	for _, src := range t.Sources {
		abs := t.ResolveSource(src)
		lang := toolchain.LanguageOf(abs)
		entry := map[string]any{
			"path":             relOrDot(f.SourceDir, abs),
			"sourceGroupIndex": 0,
		}
		if lang != "" {
			entry["compileGroupIndex"] = 0 // filled in below once groups are numbered
			groups[lang] = append(groups[lang], len(sources))
		}
		sources = append(sources, entry)
	}

	var compileGroups []map[string]any
	for i, lang := range sortedKeysOf(groups) {
		for _, si := range groups[lang] {
			sources[si]["compileGroupIndex"] = i
		}
		var includes []map[string]any
		for _, dir := range r.IncludeDirs {
			includes = append(includes, map[string]any{"path": dir})
		}
		var defines []map[string]any
		for _, d := range r.Defines {
			defines = append(defines, map[string]any{"define": d})
		}
		var fragments []map[string]any
		for _, opt := range r.CompileOpts {
			fragments = append(fragments, map[string]any{"fragment": opt, "role": "flags"})
		}
		group := map[string]any{
			"language":      lang,
			"sourceIndexes": groups[lang],
		}
		if len(includes) > 0 {
			group["includes"] = includes
		}
		if len(defines) > 0 {
			group["defines"] = defines
		}
		if len(fragments) > 0 {
			group["compileCommandFragments"] = fragments
		}
		compileGroups = append(compileGroups, group)
	}

	out := map[string]any{
		"name":             t.Name,
		"id":               targetID(t.Name, t.SourceDir),
		"type":             t.TypeName(),
		"paths":            map[string]any{"source": relOrDot(f.SourceDir, t.SourceDir), "build": relOrDot(f.BinaryDir, t.BinaryDir)},
		"sources":          sources,
		"sourceGroups":     []map[string]any{{"name": "Source Files", "sourceIndexes": indexRange(len(sources))}},
		"backtraceGraph":   map[string]any{"nodes": []any{}, "commands": []any{}, "files": []any{}},
		"codemodelVersion": map[string]any{"major": codemodelMajor, "minor": codemodelMinor},
	}
	if len(compileGroups) > 0 {
		out["compileGroups"] = compileGroups
	}
	if t.Type != "INTERFACE" && t.Type != "UTILITY" {
		output := n.targetOutput(t)
		out["nameOnDisk"] = path.Base(output)
		out["artifacts"] = []map[string]any{{"path": relOrDot(f.BinaryDir, output)}}
	}
	if deps := f.dependencyIDs(r); len(deps) > 0 {
		out["dependencies"] = deps
	}
	// A static library always reports an archive section and a linked target a
	// link section, whether or not either carries a fragment. Their presence is
	// how a client tells a library it can link against from one it cannot, so
	// omitting an empty one would answer a different question.
	switch t.Type {
	case "STATIC":
		out["archive"] = f.linkInfo(r)
	case "INTERFACE", "UTILITY":
	default:
		out["link"] = f.linkInfo(r)
	}
	if f.targetIsInstalled(t.Name) {
		out["install"] = map[string]any{
			"prefix":       map[string]any{"path": defaultPrefix(f.Graph.State)},
			"destinations": []map[string]any{{"path": defaultTargetDestination(t.Type)}},
		}
	}
	return out
}

func (f *FileAPI) dependencyIDs(r *Resolved) []map[string]any {
	var out []map[string]any
	for _, dep := range r.Deps {
		if d, ok := f.Graph.Targets[dep]; ok {
			out = append(out, map[string]any{"id": targetID(dep, d.Target.SourceDir)})
		}
	}
	return out
}

func (f *FileAPI) linkInfo(r *Resolved) map[string]any {
	var fragments []map[string]any
	for _, opt := range r.LinkOpts {
		fragments = append(fragments, map[string]any{"fragment": opt, "role": "flags"})
	}
	for _, dir := range r.LinkDirs {
		fragments = append(fragments, map[string]any{"fragment": dir, "role": "libraryPath"})
	}
	for _, lib := range r.LinkLibs {
		fragments = append(fragments, map[string]any{"fragment": lib, "role": "libraries"})
	}
	if fragments == nil {
		fragments = []map[string]any{}
	}
	info := map[string]any{"commandFragments": fragments}
	if lang := f.linkLanguage(r); lang != "" {
		info["language"] = lang
	}
	return info
}

// linkLanguage is the language whose driver links the target, which a client
// shows and which decides the standard library that gets pulled in.
func (f *FileAPI) linkLanguage(r *Resolved) string {
	best := ""
	for _, src := range r.Target.Sources {
		switch toolchain.LanguageOf(r.Target.ResolveSource(src)) {
		case "CXX":
			return "CXX"
		case "C":
			best = "C"
		}
	}
	return best
}

func (f *FileAPI) targetIsInstalled(name string) bool {
	for _, rule := range f.Graph.State.InstallRules {
		if rule.Kind != "TARGETS" {
			continue
		}
		for _, item := range rule.Items {
			if item == name {
				return true
			}
		}
	}
	return false
}

func (f *FileAPI) directory(d *eval.Directory, config string, targetIndexes []int) map[string]any {
	out := map[string]any{
		"paths": map[string]any{
			"source": relOrDot(f.SourceDir, d.Source),
			"build":  relOrDot(f.BinaryDir, d.Binary),
		},
		"backtraceGraph": map[string]any{"nodes": []any{}, "commands": []any{}, "files": []any{}},
	}
	var installers []map[string]any
	for _, rule := range f.Graph.State.InstallRules {
		if rule.SourceDir != d.Source {
			continue
		}
		installers = append(installers, map[string]any{
			"component":   componentOrDefault(rule.Component),
			"type":        strings.ToLower(rule.Kind),
			"destination": rule.Destination,
		})
	}
	if len(installers) > 0 {
		out["installers"] = installers
	}
	return out
}

func componentOrDefault(c string) string {
	if c == "" {
		return "Unspecified"
	}
	return c
}

// ----------------------------------------------------------------------------
// cache, cmakeFiles, toolchains

func (f *FileAPI) cache() map[string]any {
	var entries []map[string]any
	for _, name := range f.Graph.State.Cache.Names() {
		entry, ok := f.Graph.State.Cache.Get(name)
		if !ok {
			continue
		}
		properties := []map[string]any{
			{"name": "HELPSTRING", "value": entry.DocStr},
		}
		if entry.Advanced {
			properties = append(properties, map[string]any{"name": "ADVANCED", "value": "1"})
		}
		entries = append(entries, map[string]any{
			"name":       name,
			"value":      entry.Value,
			"type":       eval.CacheTypeName(entry.Type),
			"properties": properties,
		})
	}
	if entries == nil {
		entries = []map[string]any{}
	}
	return map[string]any{
		"kind":    "cache",
		"version": objectVersion{Major: 2, Minor: 0},
		"entries": entries,
	}
}

// cmakeFiles lists what configure read, which is how a client knows to
// re-configure when one of them changes.
func (f *FileAPI) cmakeFiles() map[string]any {
	seen := map[string]bool{}
	var inputs []map[string]any
	for _, d := range f.Graph.State.AllDirs {
		listFile := path.Join(d.Source, "CMakeLists.txt")
		if seen[listFile] {
			continue
		}
		seen[listFile] = true
		inputs = append(inputs, map[string]any{"path": relOrDot(f.SourceDir, listFile)})
	}
	if inputs == nil {
		inputs = []map[string]any{}
	}
	return map[string]any{
		"kind":    "cmakeFiles",
		"version": objectVersion{Major: 1, Minor: 1},
		"paths":   map[string]string{"source": f.SourceDir, "build": f.BinaryDir},
		"inputs":  inputs,
	}
}

func (f *FileAPI) toolchains() map[string]any {
	var out []map[string]any
	for _, lang := range sortedCompilerLanguages(f.Toolchain) {
		c := f.Toolchain.Compilers[lang]
		compiler := map[string]any{
			"path": c.Path,
			"id":   c.ID,
		}
		if c.Version != "" {
			compiler["version"] = c.Version
		}
		if dirs := f.Toolchain.SystemIncludes(); len(dirs) > 0 {
			compiler["implicit"] = map[string]any{
				"includeDirectories": dirs,
				"linkDirectories":    f.Toolchain.SystemLibDirs(),
			}
		}
		out = append(out, map[string]any{
			"language":             lang,
			"compiler":             compiler,
			"sourceFileExtensions": toolchain.ExtensionsFor(lang),
		})
	}
	if out == nil {
		out = []map[string]any{}
	}
	return map[string]any{
		"kind":       "toolchains",
		"version":    objectVersion{Major: 1, Minor: 1},
		"toolchains": out,
	}
}

func sortedCompilerLanguages(tc *toolchain.Toolchain) []string {
	out := make([]string, 0, len(tc.Compilers))
	for lang := range tc.Compilers {
		out = append(out, lang)
	}
	sort.Strings(out)
	return out
}

// ----------------------------------------------------------------------------
// helpers

func marshal(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "\t")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// contentHash names a reply file after what is in it, so that a client can tell
// whether an object changed without reading it.
func contentHash(data []byte) string {
	h := sha1.Sum(data)
	return hex.EncodeToString(h[:])[:20]
}

func indexRange(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func sortedKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func splitVersion(v string) (major, minor, patch int) {
	parts := strings.SplitN(v, ".", 3)
	get := func(i int) int {
		if i >= len(parts) {
			return 0
		}
		n := 0
		for _, c := range parts[i] {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		return n
	}
	return get(0), get(1), get(2)
}

// ParseQueries reads the query directory a client wrote into.
//
// Two forms are accepted, because clients use both: an empty file named
// <kind>-v<major> in the shared directory, and a query.json listing requests in
// a client-<name> directory. The second carries a client's own name so that two
// tools watching one build tree do not overwrite each other's answers.
func ParseQueries(list func(pattern string) ([]string, error), read func(string) ([]byte, error), binaryDir string) (map[string][]Request, error) {
	queries := map[string][]Request{}

	shared, err := list(path.Join(binaryDir, apiQueryDir, "*"))
	if err != nil {
		return nil, err
	}
	for _, entry := range shared {
		name := path.Base(strings.ReplaceAll(entry, "\\", "/"))
		if strings.HasPrefix(name, "client-") {
			data, err := read(path.Join(entry, "query.json"))
			if err != nil {
				continue
			}
			var q clientQuery
			if err := json.Unmarshal(data, &q); err != nil {
				// A malformed query is skipped rather than failing configure:
				// the client wrote it and can be told by its absence from the
				// reply, whereas a failed configure would be blamed on the
				// project.
				continue
			}
			queries[name] = q.Requests
			continue
		}
		if kind, major, ok := parseSharedQueryName(name); ok {
			queries[""] = append(queries[""], Request{Kind: kind, Version: major})
		}
	}
	if len(queries) == 0 {
		return nil, nil
	}
	return queries, nil
}

// parseSharedQueryName splits "codemodel-v2" into its kind and major version.
func parseSharedQueryName(name string) (kind string, major int, ok bool) {
	i := strings.LastIndex(name, "-v")
	if i <= 0 {
		return "", 0, false
	}
	n := 0
	digits := name[i+2:]
	if digits == "" {
		return "", 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return "", 0, false
		}
		n = n*10 + int(c-'0')
	}
	return name[:i], n, true
}
