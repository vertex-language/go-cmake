package eval

import (
	"context"
	"sort"
	"strconv"
	"strings"
)

func init() {
	register("list", cmdList)
}

// cmdList dispatches the list() command. Every sub-command names a variable
// holding a semicolon-separated string; there is no list type in CMake, only
// this convention and the commands that honour it.
func cmdList(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 2 {
		return e.fatalf("list called with incorrect number of arguments")
	}
	op, name := v[0], v[1]
	items := SplitList(e.state.GetVar(name))

	switch op {
	case "LENGTH":
		if len(v) < 3 {
			return e.fatalf("list LENGTH called with incorrect number of arguments")
		}
		e.state.SetVar(v[2], strconv.Itoa(len(items)))
		return nil

	case "GET":
		if len(v) < 4 {
			return e.fatalf("list GET called with incorrect number of arguments")
		}
		out := v[len(v)-1]
		var got []string
		for _, idxs := range v[2 : len(v)-1] {
			i, err := listIndex(idxs, len(items))
			if err != nil {
				return e.fatalf("list %v", err)
			}
			got = append(got, items[i])
		}
		e.state.SetVar(out, JoinList(got))
		return nil

	case "JOIN":
		if len(v) < 4 {
			return e.fatalf("list JOIN called with incorrect number of arguments")
		}
		e.state.SetVar(v[3], strings.Join(items, v[2]))
		return nil

	case "SUBLIST":
		if len(v) < 5 {
			return e.fatalf("list SUBLIST called with incorrect number of arguments")
		}
		begin, err := strconv.Atoi(v[2])
		if err != nil {
			return e.fatalf("list SUBLIST given non-numeric begin index")
		}
		length, err := strconv.Atoi(v[3])
		if err != nil {
			return e.fatalf("list SUBLIST given non-numeric length")
		}
		if begin < 0 || begin > len(items) {
			return e.fatalf("list SUBLIST begin index: %d out of range 0 - %d", begin, len(items))
		}
		end := len(items)
		if length >= 0 && begin+length < end {
			end = begin + length
		}
		e.state.SetVar(v[4], JoinList(items[begin:end]))
		return nil

	case "FIND":
		if len(v) < 4 {
			return e.fatalf("list FIND called with incorrect number of arguments")
		}
		idx := -1
		for i, it := range items {
			if it == v[2] {
				idx = i
				break
			}
		}
		e.state.SetVar(v[3], strconv.Itoa(idx))
		return nil

	case "APPEND":
		e.state.SetVar(name, JoinList(append(items, v[2:]...)))
		return nil

	case "PREPEND":
		e.state.SetVar(name, JoinList(append(append([]string{}, v[2:]...), items...)))
		return nil

	case "INSERT":
		if len(v) < 4 {
			return e.fatalf("list INSERT called with incorrect number of arguments")
		}
		i, err := strconv.Atoi(v[2])
		if err != nil {
			return e.fatalf("list INSERT given non-numeric index")
		}
		if i < 0 {
			i += len(items)
		}
		if i < 0 || i > len(items) {
			return e.fatalf("list INSERT index: %s out of range -%d - %d", v[2], len(items), len(items))
		}
		out := append([]string{}, items[:i]...)
		out = append(out, v[3:]...)
		out = append(out, items[i:]...)
		e.state.SetVar(name, JoinList(out))
		return nil

	case "POP_BACK":
		return listPop(e, name, items, v[2:], true)

	case "POP_FRONT":
		return listPop(e, name, items, v[2:], false)

	case "REMOVE_ITEM":
		drop := setOf(v[2:])
		out := items[:0]
		for _, it := range items {
			if !drop[it] {
				out = append(out, it)
			}
		}
		e.state.SetVar(name, JoinList(out))
		return nil

	case "REMOVE_AT":
		remove := map[int]bool{}
		for _, idxs := range v[2:] {
			i, err := listIndex(idxs, len(items))
			if err != nil {
				return e.fatalf("list %v", err)
			}
			remove[i] = true
		}
		var out []string
		for i, it := range items {
			if !remove[i] {
				out = append(out, it)
			}
		}
		e.state.SetVar(name, JoinList(out))
		return nil

	case "REMOVE_DUPLICATES":
		seen := map[string]bool{}
		var out []string
		for _, it := range items {
			if !seen[it] {
				seen[it] = true
				out = append(out, it)
			}
		}
		e.state.SetVar(name, JoinList(out))
		return nil

	case "REVERSE":
		for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
			items[i], items[j] = items[j], items[i]
		}
		e.state.SetVar(name, JoinList(items))
		return nil

	case "FILTER":
		return listFilter(e, name, items, v[2:])

	case "TRANSFORM":
		return listTransform(e, name, items, v[2:])

	case "SORT":
		return listSort(e, name, items, v[2:])
	}
	return e.fatalf("list does not recognize sub-command %s", op)
}

// listIndex resolves a possibly-negative list index, where -1 is the last
// element, and reports the range error CMake would report.
func listIndex(s string, n int) (int, error) {
	i, err := strconv.Atoi(s)
	if err != nil {
		return 0, errIndexf("index: %s is not a number", s)
	}
	if i < 0 {
		i += n
	}
	if i < 0 || i >= n {
		return 0, errIndexf("index: %s out of range (-%d, %d)", s, n, n-1)
	}
	return i, nil
}

type indexError string

func (e indexError) Error() string { return string(e) }

func errIndexf(format string, a ...any) error {
	return indexError(sprintf(format, a...))
}

func listPop(e *evaluator, name string, items, outVars []string, back bool) error {
	if len(items) == 0 {
		for _, o := range outVars {
			e.state.UnsetVar(o)
		}
		return nil
	}
	n := len(outVars)
	if n == 0 {
		n = 1
	}
	if n > len(items) {
		n = len(items)
	}
	for i := 0; i < n; i++ {
		var val string
		if back {
			val = items[len(items)-1]
			items = items[:len(items)-1]
		} else {
			val = items[0]
			items = items[1:]
		}
		if i < len(outVars) {
			e.state.SetVar(outVars[i], val)
		}
	}
	// Any output variable beyond the list's length is cleared, not left stale.
	for i := n; i < len(outVars); i++ {
		e.state.UnsetVar(outVars[i])
	}
	e.state.SetVar(name, JoinList(items))
	return nil
}

func listFilter(e *evaluator, name string, items, rest []string) error {
	if len(rest) < 3 {
		return e.fatalf("list FILTER called with incorrect number of arguments")
	}
	include := rest[0] == "INCLUDE"
	if rest[0] != "INCLUDE" && rest[0] != "EXCLUDE" {
		return e.fatalf("list FILTER expects INCLUDE or EXCLUDE, got %s", rest[0])
	}
	if rest[1] != "REGEX" {
		return e.fatalf("list FILTER expects REGEX, got %s", rest[1])
	}
	re, err := compileCMakeRegex(rest[2])
	if err != nil {
		return e.fatalf("list FILTER: %v", err)
	}
	var out []string
	for _, it := range items {
		if re.MatchString(it) == include {
			out = append(out, it)
		}
	}
	e.state.SetVar(name, JoinList(out))
	return nil
}

func listTransform(e *evaluator, name string, items, rest []string) error {
	if len(rest) == 0 {
		return e.fatalf("list TRANSFORM called with incorrect number of arguments")
	}
	action := rest[0]
	i := 1

	// Each action takes a fixed number of operands before the selector.
	var operands []string
	want := 0
	switch action {
	case "APPEND", "PREPEND":
		want = 1
	case "REPLACE":
		want = 2
	case "TOLOWER", "TOUPPER", "STRIP", "GENEX_STRIP":
		want = 0
	default:
		return e.fatalf("list TRANSFORM does not recognize action %s", action)
	}
	for ; want > 0 && i < len(rest); want, i = want-1, i+1 {
		operands = append(operands, rest[i])
	}
	if want > 0 {
		return e.fatalf("list TRANSFORM %s called with incorrect number of arguments", action)
	}

	// The selector decides which elements are transformed; the rest pass
	// through untouched, which is what makes TRANSFORM different from a
	// foreach that rebuilds the list.
	selected := make([]bool, len(items))
	for j := range selected {
		selected[j] = true
	}
	outVar := name
	for i < len(rest) {
		switch rest[i] {
		case "AT":
			for j := range selected {
				selected[j] = false
			}
			i++
			for i < len(rest) && isIndexArg(rest[i]) {
				idx, err := listIndex(rest[i], len(items))
				if err != nil {
					return e.fatalf("list TRANSFORM AT %v", err)
				}
				selected[idx] = true
				i++
			}
		case "FOR":
			for j := range selected {
				selected[j] = false
			}
			i++
			nums := []int{}
			for i < len(rest) && isIndexArg(rest[i]) {
				n, _ := strconv.Atoi(rest[i])
				nums = append(nums, n)
				i++
			}
			if len(nums) < 2 {
				return e.fatalf("list TRANSFORM FOR requires a start and a stop")
			}
			step := 1
			if len(nums) > 2 {
				step = nums[2]
			}
			if step <= 0 {
				return e.fatalf("list TRANSFORM FOR step must be positive")
			}
			for j := nums[0]; j <= nums[1] && j < len(items); j += step {
				if j >= 0 {
					selected[j] = true
				}
			}
		case "REGEX":
			if i+1 >= len(rest) {
				return e.fatalf("list TRANSFORM REGEX requires a pattern")
			}
			re, err := compileCMakeRegex(rest[i+1])
			if err != nil {
				return e.fatalf("list TRANSFORM REGEX: %v", err)
			}
			for j, it := range items {
				selected[j] = re.MatchString(it)
			}
			i += 2
		case "OUTPUT_VARIABLE":
			if i+1 >= len(rest) {
				return e.fatalf("list TRANSFORM OUTPUT_VARIABLE requires a name")
			}
			outVar = rest[i+1]
			i += 2
		default:
			return e.fatalf("list TRANSFORM does not recognize argument %s", rest[i])
		}
	}

	out := make([]string, len(items))
	copy(out, items)
	for j, it := range items {
		if !selected[j] {
			continue
		}
		switch action {
		case "APPEND":
			out[j] = it + operands[0]
		case "PREPEND":
			out[j] = operands[0] + it
		case "TOLOWER":
			out[j] = strings.ToLower(it)
		case "TOUPPER":
			out[j] = strings.ToUpper(it)
		case "STRIP":
			out[j] = strings.TrimSpace(it)
		case "GENEX_STRIP":
			out[j] = genexPattern.ReplaceAllString(it, "")
		case "REPLACE":
			re, err := compileCMakeRegex(operands[0])
			if err != nil {
				return e.fatalf("list TRANSFORM REPLACE: %v", err)
			}
			repl, _ := cmakeReplacement(operands[1])
			out[j] = re.ReplaceAllString(it, repl)
		}
	}
	e.state.SetVar(outVar, JoinList(out))
	return nil
}

func isIndexArg(s string) bool {
	if s == "" {
		return false
	}
	_, err := strconv.Atoi(s)
	return err == nil
}

func listSort(e *evaluator, name string, items, rest []string) error {
	compare, caseMode, order := "STRING", "SENSITIVE", "ASCENDING"
	for i := 0; i+1 < len(rest); i += 2 {
		switch rest[i] {
		case "COMPARE":
			compare = rest[i+1]
		case "CASE":
			caseMode = rest[i+1]
		case "ORDER":
			order = rest[i+1]
		default:
			return e.fatalf("list SORT does not recognize argument %s", rest[i])
		}
	}

	var less func(a, b string) bool
	switch compare {
	case "STRING":
		if caseMode == "INSENSITIVE" {
			less = func(a, b string) bool { return strings.ToLower(a) < strings.ToLower(b) }
		} else {
			less = func(a, b string) bool { return a < b }
		}
	case "FILE_BASENAME":
		less = func(a, b string) bool { return BaseName(a) < BaseName(b) }
	case "NATURAL":
		less = func(a, b string) bool { return naturalLess(a, b) }
	default:
		return e.fatalf("list SORT does not recognize compare method %s", compare)
	}

	// A stable sort keeps equal elements in their declared order, which matters
	// because link order is often decided by a sorted source list.
	sort.SliceStable(items, func(i, j int) bool {
		if order == "DESCENDING" {
			return less(items[j], items[i])
		}
		return less(items[i], items[j])
	})
	e.state.SetVar(name, JoinList(items))
	return nil
}

// naturalLess compares strings so that embedded runs of digits compare
// numerically: "file10" sorts after "file9", not before it.
func naturalLess(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		ai, aj := isDigit(a[i]), isDigit(b[j])
		if ai && aj {
			si, sj := i, j
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			for j < len(b) && isDigit(b[j]) {
				j++
			}
			na := strings.TrimLeft(a[si:i], "0")
			nb := strings.TrimLeft(b[sj:j], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		if a[i] != b[j] {
			return a[i] < b[j]
		}
		i++
		j++
	}
	return len(a)-i < len(b)-j
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
