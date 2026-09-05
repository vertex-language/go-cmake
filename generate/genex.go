package generate

import (
	"fmt"
	"strings"

	"github.com/vertex-language/go-cmake/eval"
	"github.com/vertex-language/go-cmake/regex"
	"github.com/vertex-language/go-cmake/toolchain"
)

// Generator expressions are the reason CMake has two phases rather than one.
//
// `$<TARGET_FILE:foo>` cannot be answered while the CMakeLists.txt is being
// read: foo may not be declared yet, and even once it is, the path it produces
// depends on the generator and the configuration. So the configure phase keeps
// these strings verbatim, and they are resolved here, where the full target
// graph and the chosen toolchain are both known.
//
// The syntax is uniform: `$<KEYWORD>` or `$<KEYWORD:arguments>`, nesting
// freely, with arguments separated by commas that a nested expression may
// contain. Getting the splitting right is most of the work — a naive split on
// ',' tears `$<IF:$<BOOL:x>,a,b>` apart at the wrong comma.

// genexContext is what an expression is evaluated against.
type genexContext struct {
	graph  *Graph
	tc     *toolchain.Toolchain
	ninja  *Ninja
	target *eval.TargetState // the target whose property is being evaluated
	config string            // the build configuration, e.g. Release
	lang   string            // the language being compiled, when known
}

// evalGenexList evaluates every entry of a list, dropping the ones that
// evaluate to nothing. An expression yielding an empty string removes its
// element rather than leaving a blank one, which is what makes
// `$<$<CONFIG:Debug>:-g>` work as a conditional flag.
func (c *genexContext) evalGenexList(items []string) ([]string, error) {
	var out []string
	for _, item := range items {
		v, err := c.eval(item)
		if err != nil {
			return nil, err
		}
		for _, part := range eval.SplitList(v) {
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out, nil
}

// eval expands every generator expression in a string.
func (c *genexContext) eval(s string) (string, error) {
	if !strings.Contains(s, "$<") {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '$' && i+1 < len(s) && s[i+1] == '<' {
			end := matchGenex(s, i)
			if end < 0 {
				return "", fmt.Errorf("unterminated generator expression in %q", s)
			}
			v, err := c.evalOne(s[i+2 : end])
			if err != nil {
				return "", err
			}
			b.WriteString(v)
			i = end + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), nil
}

// matchGenex returns the index of the '>' closing the expression that starts at
// s[i], or -1.
func matchGenex(s string, i int) int {
	depth := 0
	for j := i; j < len(s); j++ {
		switch {
		case s[j] == '$' && j+1 < len(s) && s[j+1] == '<':
			depth++
			j++
		case s[j] == '>':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// evalOne evaluates the inside of one `$<...>`.
func (c *genexContext) evalOne(inner string) (string, error) {
	keyword, args, hasArgs := splitGenexHead(inner)

	// `$<condition:value>` where the condition is itself an expression: the
	// head is not a keyword but a 0/1, and the value is kept or dropped.
	if strings.HasPrefix(keyword, "$<") {
		cond, err := c.eval(keyword)
		if err != nil {
			return "", err
		}
		if cond == "1" {
			return c.eval(args)
		}
		if cond == "0" {
			return "", nil
		}
		return "", fmt.Errorf("generator expression condition must evaluate to 0 or 1, got %q", cond)
	}

	switch keyword {
	// ---- literals and escapes ----
	case "0":
		return "", nil
	case "1":
		return c.eval(args)
	case "ANGLE-R":
		return ">", nil
	case "COMMA":
		return ",", nil
	case "SEMICOLON":
		return ";", nil

	// ---- boolean logic ----
	case "BOOL":
		v, err := c.eval(args)
		if err != nil {
			return "", err
		}
		return boolResult(!eval.IsOff(v)), nil
	case "NOT":
		v, err := c.evalBool(args)
		if err != nil {
			return "", err
		}
		return boolResult(!v), nil
	case "AND":
		parts, err := c.evalArgs(args)
		if err != nil {
			return "", err
		}
		for _, p := range parts {
			if p != "1" {
				return "0", nil
			}
		}
		return "1", nil
	case "OR":
		parts, err := c.evalArgs(args)
		if err != nil {
			return "", err
		}
		for _, p := range parts {
			if p == "1" {
				return "1", nil
			}
		}
		return "0", nil
	case "IF":
		parts, err := c.evalArgs(args)
		if err != nil {
			return "", err
		}
		if len(parts) != 3 {
			return "", fmt.Errorf("$<IF:...> requires three arguments, got %d", len(parts))
		}
		if parts[0] == "1" {
			return parts[1], nil
		}
		return parts[2], nil

	// ---- string comparison ----
	case "STREQUAL":
		parts, err := c.evalArgs(args)
		if err != nil {
			return "", err
		}
		return boolResult(len(parts) == 2 && parts[0] == parts[1]), nil
	case "EQUAL":
		parts, err := c.evalArgs(args)
		if err != nil {
			return "", err
		}
		return boolResult(len(parts) == 2 && numericEqual(parts[0], parts[1])), nil
	case "IN_LIST":
		parts, err := c.evalArgs(args)
		if err != nil {
			return "", err
		}
		if len(parts) != 2 {
			return "0", nil
		}
		for _, item := range eval.SplitList(parts[1]) {
			if item == parts[0] {
				return "1", nil
			}
		}
		return "0", nil
	case "VERSION_LESS", "VERSION_GREATER", "VERSION_EQUAL",
		"VERSION_LESS_EQUAL", "VERSION_GREATER_EQUAL":
		parts, err := c.evalArgs(args)
		if err != nil {
			return "", err
		}
		if len(parts) != 2 {
			return "0", nil
		}
		return boolResult(versionCompare(keyword, parts[0], parts[1])), nil

	// ---- string transforms ----
	case "LOWER_CASE":
		v, err := c.eval(args)
		return strings.ToLower(v), err
	case "UPPER_CASE":
		v, err := c.eval(args)
		return strings.ToUpper(v), err
	case "JOIN":
		parts, err := c.evalArgsRaw(args)
		if err != nil {
			return "", err
		}
		if len(parts) != 2 {
			return "", fmt.Errorf("$<JOIN:...> requires a list and a separator")
		}
		return strings.Join(eval.SplitList(parts[0]), parts[1]), nil
	case "REMOVE_DUPLICATES":
		v, err := c.eval(args)
		if err != nil {
			return "", err
		}
		return eval.JoinList(dedupe(eval.SplitList(v))), nil
	case "FILTER":
		return c.evalFilter(args)
	case "GENEX_EVAL":
		// The argument is expanded once to produce an expression, which is
		// then evaluated in turn. This is how a property whose value was built
		// up as a string gets its expressions honoured.
		v, err := c.eval(args)
		if err != nil {
			return "", err
		}
		return c.eval(v)

	// ---- configuration and platform ----
	case "CONFIG":
		if args == "" {
			return c.config, nil
		}
		for _, want := range splitGenexArgs(args) {
			if strings.EqualFold(strings.TrimSpace(want), c.config) {
				return "1", nil
			}
		}
		return "0", nil
	case "PLATFORM_ID":
		id := platformID()
		if args == "" {
			return id, nil
		}
		return boolResult(matchesAny(args, id)), nil
	case "C_COMPILER_ID", "CXX_COMPILER_ID":
		lang := strings.TrimSuffix(keyword, "_COMPILER_ID")
		id := ""
		if comp, ok := c.tc.Compiler(lang); ok {
			id = comp.ID
		}
		if args == "" {
			return id, nil
		}
		return boolResult(matchesAny(args, id)), nil
	case "C_COMPILER_VERSION", "CXX_COMPILER_VERSION":
		lang := strings.TrimSuffix(keyword, "_COMPILER_VERSION")
		v := ""
		if comp, ok := c.tc.Compiler(lang); ok {
			v = comp.Version
		}
		if args == "" {
			return v, nil
		}
		return boolResult(matchesAny(args, v)), nil
	case "COMPILE_LANGUAGE":
		if args == "" {
			return c.lang, nil
		}
		return boolResult(matchesAny(args, c.lang)), nil
	case "COMPILE_LANG_AND_ID":
		parts := splitGenexArgs(args)
		if len(parts) < 2 || !strings.EqualFold(parts[0], c.lang) {
			return "0", nil
		}
		id := ""
		if comp, ok := c.tc.Compiler(c.lang); ok {
			id = comp.ID
		}
		for _, want := range parts[1:] {
			if strings.EqualFold(want, id) {
				return "1", nil
			}
		}
		return "0", nil

	// ---- interface selection ----
	case "BUILD_INTERFACE":
		// Everything this package produces is a build tree, never an installed
		// one, so the build side is always the one that applies.
		return c.eval(args)
	case "INSTALL_INTERFACE":
		return "", nil
	case "BUILD_LOCAL_INTERFACE":
		return c.eval(args)
	case "INSTALL_PREFIX":
		return c.graph.State.GetVar("CMAKE_INSTALL_PREFIX"), nil

	// ---- targets ----
	case "TARGET_EXISTS":
		name, err := c.eval(args)
		if err != nil {
			return "", err
		}
		_, ok := c.graph.target(name)
		return boolResult(ok), nil
	case "TARGET_NAME_IF_EXISTS":
		name, err := c.eval(args)
		if err != nil {
			return "", err
		}
		if _, ok := c.graph.target(name); ok {
			return name, nil
		}
		return "", nil
	case "TARGET_NAME":
		return c.eval(args)
	case "TARGET_FILE", "TARGET_LINKER_FILE":
		return c.targetFile(args, keyword == "TARGET_LINKER_FILE")
	case "TARGET_FILE_NAME", "TARGET_LINKER_FILE_NAME":
		p, err := c.targetFile(args, keyword == "TARGET_LINKER_FILE_NAME")
		if err != nil {
			return "", err
		}
		return eval.BaseName(p), nil
	case "TARGET_FILE_DIR", "TARGET_LINKER_FILE_DIR":
		p, err := c.targetFile(args, keyword == "TARGET_LINKER_FILE_DIR")
		if err != nil {
			return "", err
		}
		return dirName(p), nil
	case "TARGET_FILE_BASE_NAME":
		p, err := c.targetFile(args, false)
		if err != nil {
			return "", err
		}
		return stripExt(eval.BaseName(p)), nil
	case "TARGET_PROPERTY":
		return c.targetProperty(args)
	case "TARGET_OBJECTS":
		return c.targetObjects(args)

	// ---- host and system predicates ----
	case "BOOL_TRUE":
		return "1", nil
	case "TARGET_POLICY":
		return "0", nil
	case "SHELL_PATH":
		v, err := c.eval(args)
		if err != nil {
			return "", err
		}
		return shellPath(v), nil
	case "MAKE_C_IDENTIFIER":
		v, err := c.eval(args)
		if err != nil {
			return "", err
		}
		return eval.MakeCIdentifier(v), nil
	case "PATH_EQUAL":
		parts, err := c.evalArgs(args)
		if err != nil {
			return "", err
		}
		return boolResult(len(parts) == 2 && normalise(parts[0]) == normalise(parts[1])), nil
	}

	if !hasArgs {
		return "", fmt.Errorf("unknown generator expression $<%s>", keyword)
	}
	return "", fmt.Errorf("unknown generator expression $<%s:...>", keyword)
}

// evalBool evaluates an argument that must be 0 or 1.
func (c *genexContext) evalBool(s string) (bool, error) {
	v, err := c.eval(s)
	if err != nil {
		return false, err
	}
	return v == "1", nil
}

// evalArgs splits an argument list at top-level commas and evaluates each part.
func (c *genexContext) evalArgs(s string) ([]string, error) {
	parts := splitGenexArgs(s)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		v, err := c.eval(p)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// evalArgsRaw is evalArgs for the expressions whose separator argument must not
// itself be split further.
func (c *genexContext) evalArgsRaw(s string) ([]string, error) {
	return c.evalArgs(s)
}

func (c *genexContext) evalFilter(args string) (string, error) {
	parts := splitGenexArgs(args)
	if len(parts) != 3 {
		return "", fmt.Errorf("$<FILTER:...> requires a list, INCLUDE or EXCLUDE, and a pattern")
	}
	list, err := c.eval(parts[0])
	if err != nil {
		return "", err
	}
	pattern, err := c.eval(parts[2])
	if err != nil {
		return "", err
	}
	re, err := regex.Compile(pattern)
	if err != nil {
		return "", err
	}
	include := strings.EqualFold(parts[1], "INCLUDE")
	var out []string
	for _, item := range eval.SplitList(list) {
		if re.MatchString(item) == include {
			out = append(out, item)
		}
	}
	return eval.JoinList(out), nil
}

// targetFile resolves $<TARGET_FILE:t>, or the file a consumer links against.
func (c *genexContext) targetFile(args string, linker bool) (string, error) {
	name, err := c.eval(args)
	if err != nil {
		return "", err
	}
	t, ok := c.graph.target(name)
	if !ok {
		return "", fmt.Errorf("$<TARGET_FILE:%s> refers to a target that does not exist", name)
	}
	if loc, ok := t.Properties["IMPORTED_LOCATION"]; ok && t.Imported {
		return loc, nil
	}
	if linker {
		return c.ninja.linkFileFor(t), nil
	}
	return c.ninja.targetOutput(t), nil
}

// targetProperty resolves $<TARGET_PROPERTY:tgt,prop> and its one-argument
// form, which reads the property of the target being evaluated.
func (c *genexContext) targetProperty(args string) (string, error) {
	parts := splitGenexArgs(args)
	var t *eval.TargetState
	var prop string
	switch len(parts) {
	case 1:
		if c.target == nil {
			return "", fmt.Errorf("$<TARGET_PROPERTY:%s> used outside a target", args)
		}
		t = c.target
		prop = parts[0]
	case 2:
		name, err := c.eval(parts[0])
		if err != nil {
			return "", err
		}
		found, ok := c.graph.target(name)
		if !ok {
			return "", fmt.Errorf("$<TARGET_PROPERTY:%s,...> refers to a target that does not exist", name)
		}
		t, prop = found, parts[1]
	default:
		return "", fmt.Errorf("$<TARGET_PROPERTY:...> takes one or two arguments")
	}
	v, _ := t.Property(prop)
	return v, nil
}

// targetObjects lists the object files an OBJECT library produced.
func (c *genexContext) targetObjects(args string) (string, error) {
	name, err := c.eval(args)
	if err != nil {
		return "", err
	}
	t, ok := c.graph.target(name)
	if !ok {
		return "", fmt.Errorf("$<TARGET_OBJECTS:%s> refers to a target that does not exist", name)
	}
	r, ok := c.graph.Targets[t.Name]
	if !ok {
		return "", nil
	}
	objs, err := c.ninja.objectPaths(r)
	if err != nil {
		return "", err
	}
	return eval.JoinList(objs), nil
}

// ----------------------------------------------------------------------------
// Argument splitting

// splitGenexHead separates the keyword from the arguments at the first
// top-level colon. A colon inside a nested expression or after a Windows drive
// letter does not count.
func splitGenexHead(s string) (keyword, args string, hasArgs bool) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '<':
			depth++
			i++
		case s[i] == '>':
			depth--
		case s[i] == ':' && depth == 0:
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

// splitGenexArgs splits at top-level commas.
func splitGenexArgs(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '<':
			depth++
			i++
		case s[i] == '>':
			depth--
		case s[i] == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// ----------------------------------------------------------------------------
// Small helpers

func boolResult(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func matchesAny(args, value string) bool {
	for _, want := range splitGenexArgs(args) {
		if strings.EqualFold(strings.TrimSpace(want), value) {
			return true
		}
	}
	return false
}

func dirName(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return ""
}

func stripExt(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[:i]
	}
	return name
}

func normalise(p string) string {
	return strings.TrimSuffix(strings.ReplaceAll(p, "\\", "/"), "/")
}
