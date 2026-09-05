package eval

// Argument handling: the commands a CMakeLists.txt uses to read its own
// arguments, split a command line, or ask about the host.

import (
	"context"
	"strconv"
	"strings"
)

func init() {
	register("cmake_parse_arguments", cmdParseArguments)
	register("separate_arguments", cmdSeparateArguments)
	register("site_name", cmdSiteName)
	register("cmake_host_system_information", cmdHostSystemInformation)
	register("variable_watch", cmdNoOp)
	register("include_regular_expression", cmdNoOp)
}

func cmdParseArguments(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) < 4 {
		return e.fatalf("cmake_parse_arguments called with incorrect number of arguments")
	}
	if vals[0] == "PARSE_ARGV" {
		if len(vals) < 6 {
			return e.fatalf("cmake_parse_arguments PARSE_ARGV called with incorrect number of arguments")
		}
		start, err := strconv.Atoi(vals[1])
		if err != nil {
			return e.fatalf("cmake_parse_arguments PARSE_ARGV given non-numeric index %q", vals[1])
		}
		argc, _ := strconv.Atoi(e.state.GetVar("ARGC"))
		var rest []string
		for i := start; i < argc; i++ {
			rest = append(rest, e.state.GetVar("ARGV"+strconv.Itoa(i)))
		}
		return parseArgs(e.state, vals[2], SplitList(vals[3]), SplitList(vals[4]), SplitList(vals[5]), rest)
	}
	return parseArgs(e.state, vals[0], SplitList(vals[1]), SplitList(vals[2]), SplitList(vals[3]), vals[4:])
}

// parseArgs implements the keyword-splitting that every well-behaved CMake
// function uses to read its own arguments.
func parseArgs(s *State, prefix string, options, oneValue, multiValue, args []string) error {
	isOption := setOf(options)
	isOne := setOf(oneValue)
	isMulti := setOf(multiValue)

	// Every declared keyword starts out unset, except the flags, which start
	// FALSE. A caller can then test `if(ARG_FOO)` without a DEFINED guard.
	for _, o := range options {
		s.SetVar(prefix+"_"+o, "FALSE")
	}
	for _, k := range append(append([]string{}, oneValue...), multiValue...) {
		s.UnsetVar(prefix + "_" + k)
	}

	var unparsed, missing []string
	values := map[string][]string{}
	current := ""
	currentIsOne := false
	seen := map[string]bool{}

	for _, a := range args {
		switch {
		case isOption[a]:
			s.SetVar(prefix+"_"+a, "TRUE")
			current = ""
			seen[a] = true
		case isOne[a]:
			if seen[a] {
				// A repeated one-value keyword keeps the last value.
				values[a] = nil
			}
			current, currentIsOne = a, true
			seen[a] = true
			values[a] = nil
		case isMulti[a]:
			if !seen[a] {
				values[a] = []string{}
			}
			current, currentIsOne = a, false
			seen[a] = true
		case current == "":
			unparsed = append(unparsed, a)
		case currentIsOne:
			values[current] = append(values[current], a)
			current = ""
		default:
			values[current] = append(values[current], a)
		}
	}

	for _, k := range oneValue {
		if !seen[k] {
			continue
		}
		if len(values[k]) == 0 {
			missing = append(missing, k)
			continue
		}
		s.SetVar(prefix+"_"+k, values[k][0])
	}
	for _, k := range multiValue {
		if !seen[k] {
			continue
		}
		if len(values[k]) == 0 {
			missing = append(missing, k)
			continue
		}
		s.SetVar(prefix+"_"+k, JoinList(values[k]))
	}
	s.SetVar(prefix+"_UNPARSED_ARGUMENTS", JoinList(unparsed))
	s.SetVar(prefix+"_KEYWORDS_MISSING_VALUES", JoinList(missing))
	return nil
}

func setOf(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, v := range list {
		m[v] = true
	}
	return m
}

func cmdSiteName(_ context.Context, e *evaluator, args []Arg) error {
	if len(args) == 0 {
		return nil
	}
	name := e.state.Env["COMPUTERNAME"]
	if name == "" {
		name = e.state.Env["HOSTNAME"]
	}
	if name == "" {
		name = "localhost"
	}
	e.state.Cache.Set(args[0].Val, name, CacheString, "Name of the computer/site where compile is being run", false)
	return nil
}

func cmdHostSystemInformation(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) < 3 || vals[0] != "RESULT" {
		return e.fatalf("cmake_host_system_information called with incorrect arguments")
	}
	out := vals[1]
	var results []string
	for i := 3; i < len(vals); i++ {
		results = append(results, hostInfo(e.state, vals[i]))
	}
	e.state.SetVar(out, JoinList(results))
	return nil
}

func hostInfo(s *State, key string) string {
	switch key {
	case "NUMBER_OF_LOGICAL_CORES", "NUMBER_OF_PHYSICAL_CORES":
		return strconv.Itoa(numCPU())
	case "HOSTNAME":
		if v := s.Env["COMPUTERNAME"]; v != "" {
			return v
		}
		return s.Env["HOSTNAME"]
	case "OS_NAME":
		return hostSystemName()
	case "IS_64BIT":
		return "1"
	default:
		return ""
	}
}

func cmdSeparateArguments(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) == 0 {
		return e.fatalf("separate_arguments called with incorrect number of arguments")
	}
	out := vals[0]
	if len(vals) == 1 {
		// The one-argument form re-splits the variable's own value in place.
		e.state.SetVar(out, JoinList(splitCommandLine(e.state.GetVar(out), false)))
		return nil
	}
	mode := vals[1]
	if len(vals) < 3 {
		e.state.SetVar(out, "")
		return nil
	}
	text := vals[2]
	if len(vals) > 3 && vals[2] == "PROGRAM" {
		text = vals[3]
	}
	switch mode {
	case "UNIX_COMMAND", "PROGRAM":
		e.state.SetVar(out, JoinList(splitCommandLine(text, false)))
	case "WINDOWS_COMMAND":
		e.state.SetVar(out, JoinList(splitCommandLine(text, true)))
	case "NATIVE_COMMAND":
		e.state.SetVar(out, JoinList(splitCommandLine(text, isWindows())))
	default:
		e.state.SetVar(out, JoinList(splitCommandLine(text, false)))
	}
	return nil
}

// splitCommandLine splits a command line into arguments. The Windows rules
// differ from the UNIX ones in that a backslash only escapes a quote.
func splitCommandLine(s string, windows bool) []string {
	var out []string
	var cur strings.Builder
	inArg, inQuote := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && (!windows || s[i+1] == '"' || s[i+1] == '\\'):
			cur.WriteByte(s[i+1])
			i++
			inArg = true
		case c == '"':
			inQuote = !inQuote
			inArg = true
		case !inQuote && (c == ' ' || c == '\t' || c == '\n' || c == '\r'):
			if inArg {
				out = append(out, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteByte(c)
			inArg = true
		}
	}
	if inArg {
		out = append(out, cur.String())
	}
	return out
}
