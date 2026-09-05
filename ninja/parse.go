package ninja

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// A Ninja file is a flat list of declarations over a scoped variable table.
// Parsing it is not hard, but three details are easy to get wrong and each of
// them silently corrupts a build rather than failing:
//
//   - '$' escapes. "$ " is a literal space inside a path, "$:" a literal colon,
//     "$$" a literal dollar, and "$" at end of line is a continuation. A parser
//     that splits on unescaped spaces first will tear paths in half.
//   - The three kinds of dependency. "a b | c d || e f" means explicit inputs
//     a and b, implicit inputs c and d, and order-only inputs e and f. Only the
//     explicit ones expand into $in.
//   - Variable scope. An edge's bindings are evaluated in the scope of the file
//     at the point the edge appears, then shadow the rule's bindings when the
//     rule's command is expanded. Evaluating them in the wrong order gives an
//     edge the rule's default flags instead of its own.

// Rule is a named command template.
type Rule struct {
	Name string
	Vars map[string]string
}

// Edge is one build statement: a command that turns inputs into outputs.
type Edge struct {
	Outputs         []string // explicit outputs, expanded into $out
	ImplicitOutputs []string // produced but not named on the command line
	Rule            string
	Inputs          []string // explicit inputs, expanded into $in
	Implicit        []string // inputs that force a rebuild but are not on the command line
	OrderOnly       []string // must exist first, but do not force a rebuild
	Vars            map[string]string
	Pool            string
}

// AllInputs returns every input that can make the edge dirty. Order-only
// inputs are excluded: that is what "order only" means.
func (e *Edge) AllInputs() []string {
	out := make([]string, 0, len(e.Inputs)+len(e.Implicit))
	out = append(out, e.Inputs...)
	out = append(out, e.Implicit...)
	return out
}

// AllOutputs returns every file the edge produces.
func (e *Edge) AllOutputs() []string {
	out := make([]string, 0, len(e.Outputs)+len(e.ImplicitOutputs))
	out = append(out, e.Outputs...)
	out = append(out, e.ImplicitOutputs...)
	return out
}

// File is a parsed Ninja build file.
type File struct {
	Edges    []*Edge
	Rules    map[string]*Rule
	Pools    map[string]int
	Vars     map[string]string
	Defaults []string
}

// FS is the filesystem the parser reads through, so that include and subninja
// can be resolved against something other than the real disk.
type FS interface {
	ReadFile(name string) ([]byte, error)
	Stat(name string) (fs.FileInfo, error)
}

type osFS struct{}

func (osFS) ReadFile(name string) ([]byte, error)  { return os.ReadFile(name) }
func (osFS) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }

// OSFS returns the real filesystem.
func OSFS() FS { return osFS{} }

// Parse reads a Ninja build file and everything it includes.
func Parse(filesystem FS, path string) (*File, error) {
	f := &File{
		Rules: map[string]*Rule{},
		Pools: map[string]int{},
		Vars:  map[string]string{},
	}
	// The phony rule is built in: it declares dependencies without a command.
	f.Rules["phony"] = &Rule{Name: "phony", Vars: map[string]string{}}
	if err := f.parseInto(filesystem, path, 0); err != nil {
		return nil, err
	}
	return f, nil
}

const maxIncludeDepth = 100

func (f *File) parseInto(filesystem FS, path string, depth int) error {
	if depth > maxIncludeDepth {
		return fmt.Errorf("ninja: include depth exceeded at %s", path)
	}
	data, err := filesystem.ReadFile(path)
	if err != nil {
		return err
	}
	p := &parser{file: f, fs: filesystem, path: path, depth: depth}
	return p.run(string(data))
}

type parser struct {
	file  *File
	fs    FS
	path  string
	depth int
}

func (p *parser) run(src string) error {
	lines := logicalLines(src)
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Indented lines belong to the declaration above them and are consumed
		// there; reaching one here means it had no declaration to attach to.
		if isIndented(line) {
			return fmt.Errorf("%s: unexpected indented line %q", p.path, trimmed)
		}

		switch {
		case strings.HasPrefix(trimmed, "rule "):
			name := strings.TrimSpace(trimmed[len("rule "):])
			vars, consumed := collectBindings(lines[i+1:])
			i += consumed
			p.file.Rules[name] = &Rule{Name: name, Vars: vars}

		case strings.HasPrefix(trimmed, "build "):
			edge, err := p.parseBuild(trimmed[len("build "):])
			if err != nil {
				return err
			}
			vars, consumed := collectBindings(lines[i+1:])
			i += consumed
			// An edge's own bindings are evaluated against the file's variables
			// as they stand at this point in the file.
			for k, v := range vars {
				edge.Vars[k] = p.expand(v, edge.Vars)
			}
			if pool, ok := edge.Vars["pool"]; ok {
				edge.Pool = pool
			}
			p.file.Edges = append(p.file.Edges, edge)

		case strings.HasPrefix(trimmed, "pool "):
			name := strings.TrimSpace(trimmed[len("pool "):])
			vars, consumed := collectBindings(lines[i+1:])
			i += consumed
			depth, _ := strconv.Atoi(strings.TrimSpace(vars["depth"]))
			p.file.Pools[name] = depth

		case strings.HasPrefix(trimmed, "default "):
			for _, t := range splitUnescaped(trimmed[len("default "):]) {
				p.file.Defaults = append(p.file.Defaults, unescape(t))
			}

		case strings.HasPrefix(trimmed, "include "), strings.HasPrefix(trimmed, "subninja "):
			// The two differ in variable scoping, which this parser does not
			// model separately: a subninja's variables leak into the parent,
			// which is harmless for generated files that never rely on the
			// distinction.
			rest := trimmed[strings.Index(trimmed, " ")+1:]
			target := p.expand(unescape(strings.TrimSpace(rest)), nil)
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(p.path), target)
			}
			if err := p.file.parseInto(p.fs, target, p.depth+1); err != nil {
				return err
			}

		case strings.Contains(trimmed, "="):
			k, v, _ := strings.Cut(trimmed, "=")
			p.file.Vars[strings.TrimSpace(k)] = p.expand(strings.TrimSpace(v), nil)

		default:
			return fmt.Errorf("%s: cannot parse %q", p.path, trimmed)
		}
	}
	return nil
}

// parseBuild parses the part of a build statement after the keyword:
//
//	out1 out2 | implicit_out : rule in1 | implicit || order_only
func (p *parser) parseBuild(s string) (*Edge, error) {
	colon := indexBuildColon(s)
	if colon < 0 {
		return nil, fmt.Errorf("%s: build statement has no ':'", p.path)
	}
	outputs, implicitOut := splitOnPipe(s[:colon])
	rhs := strings.TrimSpace(s[colon+1:])
	fields := splitUnescaped(rhs)
	if len(fields) == 0 {
		return nil, fmt.Errorf("%s: build statement has no rule", p.path)
	}

	edge := &Edge{
		Rule:            fields[0],
		Outputs:         p.expandAll(outputs),
		ImplicitOutputs: p.expandAll(implicitOut),
		Vars:            map[string]string{},
	}

	// The remaining fields split at "|" and "||" into the three input kinds.
	section := 0
	for _, f := range fields[1:] {
		switch f {
		case "|":
			section = 1
			continue
		case "||":
			section = 2
			continue
		}
		v := p.expand(unescape(f), nil)
		switch section {
		case 0:
			edge.Inputs = append(edge.Inputs, v)
		case 1:
			edge.Implicit = append(edge.Implicit, v)
		default:
			edge.OrderOnly = append(edge.OrderOnly, v)
		}
	}
	return edge, nil
}

func (p *parser) expandAll(items []string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, p.expand(unescape(it), nil))
	}
	return out
}

// expand substitutes $var and ${var} from the edge bindings first, then the
// file's variables.
func (p *parser) expand(s string, local map[string]string) string {
	return expandVars(s, func(name string) (string, bool) {
		if local != nil {
			if v, ok := local[name]; ok {
				return v, true
			}
		}
		v, ok := p.file.Vars[name]
		return v, ok
	})
}

// splitOnPipe divides an output list at an unescaped '|'.
func splitOnPipe(s string) (explicit, implicit []string) {
	if i := indexUnescaped(s, '|'); i >= 0 {
		return splitUnescaped(s[:i]), splitUnescaped(s[i+1:])
	}
	return splitUnescaped(s), nil
}

// ----------------------------------------------------------------------------
// Lexical helpers

// logicalLines joins '$'-continued lines, so that the rest of the parser can
// treat one declaration as one line.
func logicalLines(src string) []string {
	raw := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var out []string
	var cur strings.Builder
	joining := false
	for _, line := range raw {
		if joining {
			cur.WriteString(strings.TrimLeft(line, " \t"))
		} else {
			cur.WriteString(line)
		}
		s := cur.String()
		if endsWithContinuation(s) {
			cur.Reset()
			cur.WriteString(s[:len(s)-1])
			joining = true
			continue
		}
		out = append(out, s)
		cur.Reset()
		joining = false
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// endsWithContinuation reports whether a line ends in an unescaped '$'.
func endsWithContinuation(s string) bool {
	if !strings.HasSuffix(s, "$") {
		return false
	}
	// Count the trailing run of '$': an odd count means the last one is a
	// continuation, an even count means they are escaped dollars.
	n := 0
	for i := len(s) - 1; i >= 0 && s[i] == '$'; i-- {
		n++
	}
	return n%2 == 1
}

func isIndented(line string) bool {
	return line != "" && (line[0] == ' ' || line[0] == '\t')
}

// collectBindings reads the indented "key = value" lines that follow a
// declaration, returning them and how many lines were consumed.
func collectBindings(lines []string) (map[string]string, int) {
	vars := map[string]string{}
	n := 0
	for _, line := range lines {
		if !isIndented(line) {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				// A blank line does not end the block; ninja allows one only at
				// the end, but tolerating it costs nothing.
				n++
				continue
			}
			break
		}
		n++
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		k, v, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		vars[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	// Trailing blank lines were counted but belong to nobody; give them back.
	for n > 0 && strings.TrimSpace(lines[n-1]) == "" {
		n--
	}
	return vars, n
}

// indexBuildColon finds the ':' that separates a build statement's outputs from
// its rule.
//
// Ninja's own answer is that a colon inside a path must be written "$:", and
// this package's generator does escape them. But a Windows path's drive colon
// is unambiguous — a single letter followed by ':' and a separator can only be
// a drive — and a hand-written build file that omits the escape is far better
// served by working than by a message about an unknown rule named "C:/...".
func indexBuildColon(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '$' {
			i++
			continue
		}
		if s[i] != ':' {
			continue
		}
		if isDriveColon(s, i) {
			continue
		}
		return i
	}
	return -1
}

// isDriveColon reports whether the colon at s[i] belongs to a drive letter.
func isDriveColon(s string, i int) bool {
	if i == 0 || i+1 >= len(s) {
		return false
	}
	c := s[i-1]
	isAlpha := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	if !isAlpha {
		return false
	}
	// The letter must start a path component, and a separator must follow.
	if i-2 >= 0 {
		prev := s[i-2]
		if prev != ' ' && prev != '\t' && prev != '/' && prev != '\\' {
			return false
		}
	}
	return s[i+1] == '/' || s[i+1] == '\\'
}

// indexUnescaped finds the first occurrence of c not preceded by '$'.
func indexUnescaped(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '$' {
			i++ // skip the escaped character
			continue
		}
		if s[i] == c {
			return i
		}
	}
	return -1
}

// splitUnescaped splits on whitespace that is not '$'-escaped.
func splitUnescaped(s string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '$' && i+1 < len(s) {
			cur.WriteByte(s[i])
			cur.WriteByte(s[i+1])
			i++
			continue
		}
		if s[i] == ' ' || s[i] == '\t' {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(s[i])
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// unescape resolves the '$' escapes in a path.
func unescape(s string) string {
	if !strings.Contains(s, "$") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case ' ', ':', '$':
			b.WriteByte(s[i])
		default:
			// Not an escape: a variable reference, which the caller expands.
			b.WriteByte('$')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// expandVars substitutes $name and ${name} using lookup.
func expandVars(s string, lookup func(string) (string, bool)) string {
	if !strings.Contains(s, "$") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch {
		case s[i] == '$':
			b.WriteByte('$')
		case s[i] == ' ' || s[i] == ':':
			b.WriteByte(s[i])
		case s[i] == '{':
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				b.WriteByte('$')
				b.WriteByte(s[i])
				continue
			}
			name := s[i+1 : i+end]
			if v, ok := lookup(name); ok {
				b.WriteString(v)
			}
			i += end
		default:
			start := i
			for i < len(s) && isVarChar(s[i]) {
				i++
			}
			name := s[start:i]
			i--
			if name == "" {
				b.WriteByte('$')
				continue
			}
			if v, ok := lookup(name); ok {
				b.WriteString(v)
			}
		}
	}
	return b.String()
}

func isVarChar(c byte) bool {
	return c == '_' || c == '-' || c == '.' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
