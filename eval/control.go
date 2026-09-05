package eval

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/vertex-language/go-cmake/ast"
)

// controlKeywords are the command names handled by the evaluator itself rather
// than by an entry in the command table, because they need the unexpanded AST.
var controlKeywords = map[string]bool{
	"if": true, "elseif": true, "else": true, "endif": true,
	"foreach": true, "endforeach": true,
	"while": true, "endwhile": true,
	"function": true, "endfunction": true,
	"macro": true, "endmacro": true,
	"block": true, "endblock": true,
	"break": true, "continue": true, "return": true,
}

// blockOpeners maps a block-opening command to the command that closes it.
var blockOpeners = map[string]string{
	"if":       "endif",
	"foreach":  "endforeach",
	"while":    "endwhile",
	"function": "endfunction",
	"macro":    "endmacro",
	"block":    "endblock",
}

var blockClosers = map[string]bool{
	"endif": true, "endforeach": true, "endwhile": true,
	"endfunction": true, "endmacro": true, "endblock": true,
}

// matchBlock returns the index of the command that closes the block opened at
// stmts[open]. Nesting of every block kind is tracked with a stack so that a
// mismatched terminator is reported where it occurs rather than silently
// swallowing the rest of the file.
func matchBlock(stmts []ast.Stmt, open int) (int, error) {
	name := cmdName(stmts[open])
	want := blockOpeners[name]
	stack := []string{want}
	for i := open + 1; i < len(stmts); i++ {
		n := cmdName(stmts[i])
		if n == "" {
			continue
		}
		if closer, ok := blockOpeners[n]; ok {
			stack = append(stack, closer)
			continue
		}
		if blockClosers[n] {
			top := stack[len(stack)-1]
			if n != top {
				return 0, fmt.Errorf("%s: expected %s, got %s", name, top, n)
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("%s: missing %s", name, want)
}

// cmdName returns the lowercased command name of a statement, or "" if the
// statement is not a command invocation.
func cmdName(s ast.Stmt) string {
	c, ok := s.(*ast.CommandInvocation)
	if !ok {
		return ""
	}
	return strings.ToLower(c.Name.Lit)
}

// invocation returns the statement as a command invocation, or nil.
func invocation(s ast.Stmt) *ast.CommandInvocation {
	c, _ := s.(*ast.CommandInvocation)
	return c
}

// ----------------------------------------------------------------------------
// if / elseif / else

// ifClause is one branch of an if block. Cond is nil for the else branch.
type ifClause struct {
	Cond []ast.Arg
	Body []ast.Stmt
}

// splitIf divides an if block into its clauses. stmts[open] is the if and
// stmts[end] is the endif.
func splitIf(stmts []ast.Stmt, open, end int) ([]ifClause, error) {
	var clauses []ifClause
	cur := ifClause{Cond: invocation(stmts[open]).Args}
	depth := 0
	bodyStart := open + 1
	for i := open + 1; i < end; i++ {
		n := cmdName(stmts[i])
		if _, ok := blockOpeners[n]; ok {
			depth++
			continue
		}
		if blockClosers[n] {
			depth--
			continue
		}
		if depth != 0 {
			continue
		}
		switch n {
		case "elseif":
			cur.Body = stmts[bodyStart:i]
			clauses = append(clauses, cur)
			cur = ifClause{Cond: invocation(stmts[i]).Args}
			bodyStart = i + 1
		case "else":
			cur.Body = stmts[bodyStart:i]
			clauses = append(clauses, cur)
			cur = ifClause{Cond: nil}
			bodyStart = i + 1
		}
	}
	cur.Body = stmts[bodyStart:end]
	clauses = append(clauses, cur)
	return clauses, nil
}

func (e *evaluator) evalIf(ctx context.Context, stmts []ast.Stmt, open, end int) error {
	clauses, err := splitIf(stmts, open, end)
	if err != nil {
		return err
	}
	for i, c := range clauses {
		if c.Cond == nil && i > 0 {
			// else: no condition, always taken if reached.
			return e.evalStmts(ctx, c.Body)
		}
		name := "if"
		if i > 0 {
			name = "elseif"
		}
		ok, err := e.state.EvalCondition(name, e.expand(c.Cond), e.fs)
		if err != nil {
			return e.fatalf("%s", err)
		}
		if ok {
			return e.evalStmts(ctx, c.Body)
		}
	}
	return nil
}

// ----------------------------------------------------------------------------
// foreach

func (e *evaluator) evalForeach(ctx context.Context, stmts []ast.Stmt, open, end int) error {
	args := e.expand(invocation(stmts[open]).Args)
	body := stmts[open+1 : end]
	if len(args) == 0 {
		return fmt.Errorf("foreach called with incorrect number of arguments")
	}

	// foreach(v1 v2 ... IN ZIP_LISTS l1 l2 ...) binds several variables at once;
	// every other form binds exactly one.
	vars, rows, err := e.foreachRows(args)
	if err != nil {
		return err
	}

	// The loop variables shadow whatever was there and are restored afterwards,
	// which is what makes a foreach variable invisible outside its loop.
	saved := make([]struct {
		val string
		ok  bool
	}, len(vars))
	for i, v := range vars {
		saved[i].val, saved[i].ok = e.state.Current.Get(v)
	}
	defer func() {
		for i, v := range vars {
			if saved[i].ok {
				e.state.Current.Set(v, saved[i].val)
			} else {
				e.state.Current.Unset(v)
			}
		}
	}()

	for _, row := range rows {
		for i, v := range vars {
			if i < len(row) && row[i].ok {
				e.state.Current.Set(v, row[i].val)
			} else {
				// A ZIP_LISTS row shorter than the longest list leaves the
				// remaining variables undefined for that iteration.
				e.state.Current.Unset(v)
			}
		}
		err := e.evalStmts(ctx, body)
		if err != nil {
			if _, ok := err.(breakSignal); ok {
				return nil
			}
			if _, ok := err.(continueSignal); ok {
				continue
			}
			return err
		}
	}
	return nil
}

type cell struct {
	val string
	ok  bool
}

// foreachRows resolves a foreach argument list into the loop variables and the
// sequence of value rows to bind to them.
func (e *evaluator) foreachRows(args []Arg) ([]string, [][]cell, error) {
	vals := Args(args)

	// foreach(var RANGE ...)
	if len(vals) >= 3 && vals[1] == "RANGE" {
		nums := vals[2:]
		var start, stop int64 = 0, 0
		var step int64 = 1
		var err error
		switch len(nums) {
		case 1:
			stop, err = strconv.ParseInt(nums[0], 10, 64)
		case 2:
			start, err = strconv.ParseInt(nums[0], 10, 64)
			if err == nil {
				stop, err = strconv.ParseInt(nums[1], 10, 64)
			}
		case 3:
			start, err = strconv.ParseInt(nums[0], 10, 64)
			if err == nil {
				stop, err = strconv.ParseInt(nums[1], 10, 64)
			}
			if err == nil {
				step, err = strconv.ParseInt(nums[2], 10, 64)
			}
		default:
			return nil, nil, fmt.Errorf("foreach RANGE called with incorrect number of arguments")
		}
		if err != nil {
			return nil, nil, fmt.Errorf("foreach RANGE: expected a number, got a non-numeric argument")
		}
		if step == 0 {
			return nil, nil, fmt.Errorf("foreach RANGE: step may not be 0")
		}
		var rows [][]cell
		if step > 0 {
			for i := start; i <= stop; i += step {
				rows = append(rows, []cell{{strconv.FormatInt(i, 10), true}})
			}
		} else {
			for i := start; i >= stop; i += step {
				rows = append(rows, []cell{{strconv.FormatInt(i, 10), true}})
			}
		}
		return vals[:1], rows, nil
	}

	// foreach(... IN [LISTS ...] [ITEMS ...]) and IN ZIP_LISTS.
	in := -1
	for i, v := range vals {
		if v == "IN" {
			in = i
			break
		}
	}
	if in < 0 {
		// Plain form: foreach(var item...)
		rows := make([][]cell, 0, len(vals)-1)
		for _, v := range vals[1:] {
			rows = append(rows, []cell{{v, true}})
		}
		return vals[:1], rows, nil
	}

	loopVars := vals[:in]
	if len(loopVars) == 0 {
		return nil, nil, fmt.Errorf("foreach called with incorrect number of arguments")
	}
	rest := vals[in+1:]

	if len(rest) > 0 && rest[0] == "ZIP_LISTS" {
		lists := rest[1:]
		if len(loopVars) != 1 && len(loopVars) != len(lists) {
			return nil, nil, fmt.Errorf("foreach ZIP_LISTS: expected %d variables, got %d", len(lists), len(loopVars))
		}
		cols := make([][]string, len(lists))
		max := 0
		for i, name := range lists {
			cols[i] = SplitList(e.state.GetVar(name))
			if len(cols[i]) > max {
				max = len(cols[i])
			}
		}
		// A single loop variable becomes var_0, var_1, ... one per list.
		vars := loopVars
		if len(loopVars) == 1 && len(lists) > 0 {
			vars = make([]string, len(lists))
			for i := range lists {
				vars[i] = loopVars[0] + "_" + strconv.Itoa(i)
			}
		}
		rows := make([][]cell, 0, max)
		for r := 0; r < max; r++ {
			row := make([]cell, len(cols))
			for c := range cols {
				if r < len(cols[c]) {
					row[c] = cell{cols[c][r], true}
				}
			}
			rows = append(rows, row)
		}
		return vars, rows, nil
	}

	if len(loopVars) != 1 {
		return nil, nil, fmt.Errorf("foreach: only ZIP_LISTS accepts multiple loop variables")
	}
	var items []string
	mode := ""
	for _, v := range rest {
		switch v {
		case "LISTS", "ITEMS":
			mode = v
			continue
		}
		switch mode {
		case "LISTS":
			// An undefined or empty list contributes nothing, not an empty item.
			if val, ok := e.state.Current.Get(v); ok {
				items = append(items, SplitList(val)...)
			} else if ent, ok := e.state.Cache.Get(v); ok {
				items = append(items, SplitList(ent.Value)...)
			}
		default:
			items = append(items, v)
		}
	}
	rows := make([][]cell, 0, len(items))
	for _, it := range items {
		rows = append(rows, []cell{{it, true}})
	}
	return loopVars, rows, nil
}

// ----------------------------------------------------------------------------
// while

func (e *evaluator) evalWhile(ctx context.Context, stmts []ast.Stmt, open, end int) error {
	cond := invocation(stmts[open]).Args
	body := stmts[open+1 : end]
	// A guard against a runaway condition: real CMake will spin forever, but a
	// library embedded in another program should fail loudly instead.
	const maxIterations = 1 << 22
	for n := 0; ; n++ {
		if n > maxIterations {
			return fmt.Errorf("while loop did not terminate after %d iterations", maxIterations)
		}
		ok, err := e.state.EvalCondition("while", e.expand(cond), e.fs)
		if err != nil {
			return e.fatalf("%s", err)
		}
		if !ok {
			return nil
		}
		err = e.evalStmts(ctx, body)
		if err != nil {
			if _, ok := err.(breakSignal); ok {
				return nil
			}
			if _, ok := err.(continueSignal); ok {
				continue
			}
			return err
		}
	}
}

// ----------------------------------------------------------------------------
// block / endblock

func (e *evaluator) evalBlockScope(ctx context.Context, stmts []ast.Stmt, open, end int) error {
	args := Args(e.expand(invocation(stmts[open]).Args))
	var propagate []string
	scopeForVariables := true
	mode := ""
	for _, a := range args {
		switch a {
		case "SCOPE_FOR", "PROPAGATE":
			mode = a
			continue
		}
		switch mode {
		case "SCOPE_FOR":
			// block(SCOPE_FOR POLICIES) leaves variables in the enclosing scope.
			if a == "VARIABLES" {
				scopeForVariables = true
			}
		case "PROPAGATE":
			propagate = append(propagate, a)
		}
	}
	if mode == "SCOPE_FOR" || (mode == "" && len(args) == 0) {
		// Default with no SCOPE_FOR is a variable scope.
	}
	if len(args) > 0 && !containsStr(args, "VARIABLES") && containsStr(args, "POLICIES") {
		scopeForVariables = false
	}

	if !scopeForVariables {
		return e.evalStmts(ctx, stmts[open+1:end])
	}

	outer := e.state.Current
	e.state.Current = NewScope(BlockScope, outer)
	err := e.evalStmts(ctx, stmts[open+1:end])
	inner := e.state.Current
	e.state.Current = outer
	for _, name := range propagate {
		if v, ok := inner.Get(name); ok {
			outer.Set(name, v)
		} else {
			outer.Unset(name)
		}
	}
	return err
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------------------
// function / macro definition

func (e *evaluator) defineFunction(stmts []ast.Stmt, open, end int) error {
	args := Args(e.expand(invocation(stmts[open]).Args))
	if len(args) == 0 {
		return fmt.Errorf("function called with incorrect number of arguments")
	}
	e.state.Functions[strings.ToLower(args[0])] = &FunctionDef{
		Name:   args[0],
		Params: args[1:],
		Body:   stmts[open+1 : end],
	}
	return nil
}

func (e *evaluator) defineMacro(stmts []ast.Stmt, open, end int) error {
	args := Args(e.expand(invocation(stmts[open]).Args))
	if len(args) == 0 {
		return fmt.Errorf("macro called with incorrect number of arguments")
	}
	e.state.Macros[strings.ToLower(args[0])] = &MacroDef{
		Name:   args[0],
		Params: args[1:],
		Body:   stmts[open+1 : end],
	}
	return nil
}

// ----------------------------------------------------------------------------
// function / macro invocation

// callFunction runs a user-defined function in a fresh scope whose parent is
// the caller's scope. CMake's function scoping is dynamic: a function can read
// any variable visible at the call site, but its own assignments are discarded
// on return unless made with PARENT_SCOPE.
func (e *evaluator) callFunction(ctx context.Context, fn *FunctionDef, args []Arg) error {
	outer := e.state.Current
	scope := NewScope(FunctionScope, outer)
	bindCallArgs(scope, fn.Params, args)
	scope.Set("CMAKE_CURRENT_FUNCTION", fn.Name)

	e.state.Current = scope
	e.state.callDepth++
	defer func() {
		e.state.callDepth--
		e.state.Current = outer
	}()
	if e.state.callDepth > maxCallDepth {
		return fmt.Errorf("maximum recursion depth of %d exceeded in %s", maxCallDepth, fn.Name)
	}

	err := e.evalStmts(ctx, fn.Body)
	if r, ok := err.(returnSignal); ok {
		// return(PROPAGATE ...) lifts named variables into the caller's scope.
		for _, name := range r.propagate {
			if v, ok := scope.Get(name); ok {
				outer.Set(name, v)
			} else {
				outer.Unset(name)
			}
		}
		return nil
	}
	return err
}

const maxCallDepth = 1000

// callMacro runs a user-defined macro. A macro is not a function: it has no
// scope of its own, and its parameters are substituted textually into the body
// before expansion. That is why `if(DEFINED ARGV0)` is false inside a macro and
// why `foreach(x IN LISTS ARGN)` does not work there — ARGN is not a variable,
// it is a string that gets pasted in.
func (e *evaluator) callMacro(ctx context.Context, mac *MacroDef, args []Arg) error {
	repl := macroSubstitutions(mac.Params, args)
	e.state.callDepth++
	defer func() { e.state.callDepth-- }()
	if e.state.callDepth > maxCallDepth {
		return fmt.Errorf("maximum recursion depth of %d exceeded in %s", maxCallDepth, mac.Name)
	}

	body := substituteStmts(mac.Body, repl)
	err := e.evalStmts(ctx, body)
	if _, ok := err.(returnSignal); ok {
		// return() inside a macro returns from the enclosing function or file,
		// not from the macro, so the signal is passed through.
		return err
	}
	return err
}

// bindCallArgs sets the named parameters and the ARG* variables of a function.
func bindCallArgs(scope *Scope, params []string, args []Arg) {
	vals := Args(args)
	for i, p := range params {
		if i < len(vals) {
			scope.Set(p, vals[i])
		} else {
			scope.Set(p, "")
		}
	}
	scope.Set("ARGC", strconv.Itoa(len(vals)))
	scope.Set("ARGV", JoinList(vals))
	for i, v := range vals {
		scope.Set("ARGV"+strconv.Itoa(i), v)
	}
	if len(vals) > len(params) {
		scope.Set("ARGN", JoinList(vals[len(params):]))
	} else {
		scope.Set("ARGN", "")
	}
}

// macroSubstitutions builds the textual replacement table for a macro body.
func macroSubstitutions(params []string, args []Arg) map[string]string {
	vals := Args(args)
	repl := make(map[string]string, len(params)+len(vals)+3)
	for i, p := range params {
		if i < len(vals) {
			repl[p] = vals[i]
		} else {
			repl[p] = ""
		}
	}
	repl["ARGC"] = strconv.Itoa(len(vals))
	repl["ARGV"] = JoinList(vals)
	for i, v := range vals {
		repl["ARGV"+strconv.Itoa(i)] = v
	}
	if len(vals) > len(params) {
		repl["ARGN"] = JoinList(vals[len(params):])
	} else {
		repl["ARGN"] = ""
	}
	return repl
}

// substituteStmts returns a copy of a macro body with ${param} references
// replaced by their argument text.
func substituteStmts(body []ast.Stmt, repl map[string]string) []ast.Stmt {
	out := make([]ast.Stmt, len(body))
	for i, s := range body {
		c := invocation(s)
		if c == nil {
			out[i] = s
			continue
		}
		nc := *c
		nc.Args = make([]ast.Arg, len(c.Args))
		for j, a := range c.Args {
			nc.Args[j] = substituteArg(a, repl)
		}
		out[i] = &nc
	}
	return out
}

func substituteArg(a ast.Arg, repl map[string]string) ast.Arg {
	switch n := a.(type) {
	case *ast.QuotedArg:
		return &ast.QuotedArg{TokPos: n.TokPos, Lit: substituteText(n.Lit, repl)}
	case *ast.UnquotedArg:
		return &ast.UnquotedArg{TokPos: n.TokPos, Lit: substituteText(n.Lit, repl)}
	default:
		// Bracket arguments are literal in CMake and are not substituted.
		return a
	}
}

// substituteText replaces ${NAME} and @NAME@ occurrences whose NAME is a macro
// parameter. Names that are not macro parameters are left alone so that they
// are resolved later as ordinary variables.
func substituteText(s string, repl map[string]string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '\\' && i+1 < len(s) {
			b.WriteByte(s[i])
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		if s[i] == '$' && i+1 < len(s) && s[i+1] == '{' {
			if end := strings.IndexByte(s[i+2:], '}'); end >= 0 {
				name := s[i+2 : i+2+end]
				if v, ok := repl[name]; ok {
					b.WriteString(v)
					i += 2 + end + 1
					continue
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
