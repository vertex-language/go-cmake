package cmake_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The File API is a contract with programs nobody here controls: VS Code's
// CMake Tools, CLion, Qt Creator. A client that finds a malformed reply does
// not report a CMake problem -- it reports that the project has no targets, and
// the person looking at it has no reason to suspect the build tool.
//
// So these tests walk the reply the way a client does: read the index, follow
// every jsonFile it names, and require that each one exists, parses, and says
// what the index promised.

// apiProject configures a project with a File API query and returns the reply
// directory.
func apiProject(t *testing.T, query string) (source, reply string) {
	t.Helper()
	source = t.TempDir()
	write := func(name, content string) {
		full := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("CMakeLists.txt", `
cmake_minimum_required(VERSION 3.16)
project(Demo VERSION 1.2.3 LANGUAGES C)
add_library(util STATIC src/util.c)
target_include_directories(util PUBLIC inc)
target_compile_definitions(util PRIVATE UTIL_BUILD=1)
add_executable(app src/main.c)
target_link_libraries(app PRIVATE util)
install(TARGETS app)
add_subdirectory(sub)
`)
	write("inc/util.h", "int a(void);\n")
	write("src/util.c", "int a(void){return 1;}\n")
	write("src/main.c", "int main(void){return 0;}\n")
	write("sub/CMakeLists.txt", "add_library(extra STATIC extra.c)\n")
	write("sub/extra.c", "int e(void){return 2;}\n")

	build := filepath.Join(source, "b")
	queryDir := filepath.Join(build, ".cmake", "api", "v1", "query", "client-test")
	if err := os.MkdirAll(queryDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queryDir, "query.json"), []byte(query), 0644); err != nil {
		t.Fatal(err)
	}

	if code, out, errOut := runCLI(t, source, "-S", source, "-B", build); code != 0 {
		t.Fatalf("configure failed:\n%s%s", out, errOut)
	}
	return source, filepath.Join(build, ".cmake", "api", "v1", "reply")
}

// readIndex finds and parses the index, the way a client does.
func readIndex(t *testing.T, reply string) map[string]any {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(reply, "index-*.json"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no index was written in %s", reply)
	}
	if len(entries) > 1 {
		// A second index means the previous reply was not cleared, and a client
		// picking the newest would follow names into stale objects.
		t.Errorf("more than one index is present: %v", entries)
	}
	return readJSON(t, entries[0])
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}
	return out
}

const fullQuery = `{"requests":[
  {"kind":"codemodel","version":2},
  {"kind":"cache","version":2},
  {"kind":"toolchains","version":1},
  {"kind":"cmakeFiles","version":1}]}`

// TestFileAPIReplyIsWalkable is the test that matters: every path the index
// names has to lead somewhere.
func TestFileAPIReplyIsWalkable(t *testing.T) {
	_, reply := apiProject(t, fullQuery)
	index := readIndex(t, reply)

	objects, _ := index["objects"].([]any)
	if len(objects) != 4 {
		t.Fatalf("want four objects, got %d: %v", len(objects), objects)
	}
	var kinds []string
	for _, o := range objects {
		obj := o.(map[string]any)
		kinds = append(kinds, obj["kind"].(string))
		follow(t, reply, obj["jsonFile"].(string))
	}
	sort.Strings(kinds)
	if got := strings.Join(kinds, ","); got != "cache,cmakeFiles,codemodel,toolchains" {
		t.Errorf("objects = %s", got)
	}

	// The client's own section must name the same objects back.
	replies, _ := index["reply"].(map[string]any)
	client, ok := replies["client-test"].(map[string]any)
	if !ok {
		t.Fatalf("the client's section is missing: %v", replies)
	}
	q := client["query.json"].(map[string]any)
	if len(q["responses"].([]any)) != 4 {
		t.Errorf("the client got %v responses, want 4", len(q["responses"].([]any)))
	}
}

func follow(t *testing.T, reply, name string) map[string]any {
	t.Helper()
	full := filepath.Join(reply, name)
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("the index names %s, which is not there", name)
	}
	return readJSON(t, full)
}

// TestFileAPICodemodelDescribesTheProject follows the codemodel down to each
// target file, which is the path a client actually takes.
func TestFileAPICodemodelDescribesTheProject(t *testing.T) {
	_, reply := apiProject(t, fullQuery)
	index := readIndex(t, reply)

	var codemodelFile string
	for _, o := range index["objects"].([]any) {
		if obj := o.(map[string]any); obj["kind"] == "codemodel" {
			codemodelFile = obj["jsonFile"].(string)
		}
	}
	cm := follow(t, reply, codemodelFile)

	configs := cm["configurations"].([]any)
	if len(configs) != 1 {
		t.Fatalf("want one configuration, got %d", len(configs))
	}
	config := configs[0].(map[string]any)

	// Every target the project declared, including the one in a subdirectory.
	byName := map[string]map[string]any{}
	for _, tr := range config["targets"].([]any) {
		ref := tr.(map[string]any)
		byName[ref["name"].(string)] = follow(t, reply, ref["jsonFile"].(string))
	}
	for _, want := range []string{"util", "app", "extra"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("target %q is missing from the codemodel", want)
		}
	}

	// Every directory file the codemodel names must exist too.
	for _, dr := range config["directories"].([]any) {
		follow(t, reply, dr.(map[string]any)["jsonFile"].(string))
	}

	util := byName["util"]
	if util["type"] != "STATIC_LIBRARY" {
		t.Errorf("util type = %v", util["type"])
	}

	// The compile group is what tells an editor how to parse the file. Without
	// the include directory and the definition, it reports errors the compiler
	// does not.
	groups, _ := util["compileGroups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("want one compile group for util, got %v", groups)
	}
	group := groups[0].(map[string]any)
	if group["language"] != "C" {
		t.Errorf("compile group language = %v", group["language"])
	}
	if !hasEntry(group["includes"], "path", "inc") {
		t.Errorf("the public include directory is missing: %v", group["includes"])
	}
	if !hasEntry(group["defines"], "define", "UTIL_BUILD=1") {
		t.Errorf("the private definition is missing: %v", group["defines"])
	}

	app := byName["app"]
	if app["type"] != "EXECUTABLE" {
		t.Errorf("app type = %v", app["type"])
	}
	if app["install"] == nil {
		t.Error("app is installed but the target says nothing about it")
	}
	if app["dependencies"] == nil {
		t.Error("app links util but declares no dependency on it")
	}
}

// hasEntry reports whether a list of objects has one whose field contains want.
func hasEntry(list any, field, want string) bool {
	entries, ok := list.([]any)
	if !ok {
		return false
	}
	for _, e := range entries {
		if v, ok := e.(map[string]any)[field].(string); ok && strings.Contains(v, want) {
			return true
		}
	}
	return false
}

// TestFileAPISharedQueryForm covers the other way a client asks: an empty file
// named for the object, used by tools that keep no state of their own.
func TestFileAPISharedQueryForm(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "CMakeLists.txt"), []byte(`
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
add_executable(app main.c)
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.c"), []byte("int main(void){return 0;}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	build := filepath.Join(source, "b")
	queryDir := filepath.Join(build, ".cmake", "api", "v1", "query")
	if err := os.MkdirAll(queryDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codemodel-v2", "cache-v2"} {
		if err := os.WriteFile(filepath.Join(queryDir, name), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}

	runCLI(t, source, "-S", source, "-B", build)
	index := readIndex(t, filepath.Join(build, ".cmake", "api", "v1", "reply"))

	reply := index["reply"].(map[string]any)
	for _, want := range []string{"codemodel-v2", "cache-v2"} {
		if _, ok := reply[want]; !ok {
			t.Errorf("the shared query %q went unanswered: %v", want, reply)
		}
	}
}

// TestFileAPIIsSilentWithoutAQuery covers the default. A reply nobody asked for
// leaves an index that the next client reads as current.
func TestFileAPIIsSilentWithoutAQuery(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "CMakeLists.txt"), []byte(`
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES NONE)
`), 0644); err != nil {
		t.Fatal(err)
	}
	build := filepath.Join(source, "b")
	runCLI(t, source, "-S", source, "-B", build)

	if _, err := os.Stat(filepath.Join(build, ".cmake", "api", "v1", "reply")); err == nil {
		t.Error("a reply was written without a query")
	}
}

// TestFileAPIReplyIsReplaced covers the stale-index hazard directly.
func TestFileAPIReplyIsReplaced(t *testing.T) {
	source, reply := apiProject(t, fullQuery)
	first := readIndex(t, reply)

	// Change the project so the objects must change, then configure again.
	listFile := filepath.Join(source, "CMakeLists.txt")
	data, err := os.ReadFile(listFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(listFile, append(data, []byte("\nadd_library(third STATIC src/util.c)\n")...), 0644); err != nil {
		t.Fatal(err)
	}
	runCLI(t, source, "-S", source, "-B", filepath.Join(source, "b"))

	second := readIndex(t, reply) // fails if two indexes are present
	if fmtObjects(first) == fmtObjects(second) {
		t.Error("the reply did not change after the project did")
	}
	for _, o := range second["objects"].([]any) {
		follow(t, reply, o.(map[string]any)["jsonFile"].(string))
	}
}

func fmtObjects(index map[string]any) string {
	var out []string
	for _, o := range index["objects"].([]any) {
		out = append(out, o.(map[string]any)["jsonFile"].(string))
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}
