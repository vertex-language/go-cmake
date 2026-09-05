package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// strJSON implements string(JSON ...), CMake's JSON reader and writer.
//
// The sub-command reports failures through an optional ERROR_VARIABLE rather
// than by stopping the configure run, so a project can probe a document it is
// not sure about. When no ERROR_VARIABLE is given, an error is fatal.
func strJSON(e *evaluator, v []string) error {
	if len(v) < 2 {
		return e.fatalf("string JSON called with incorrect number of arguments")
	}
	out := v[1]
	i := 2
	errVar := ""
	if i < len(v) && v[i] == "ERROR_VARIABLE" {
		if i+1 >= len(v) {
			return e.fatalf("string JSON ERROR_VARIABLE requires a variable name")
		}
		errVar = v[i+1]
		i += 2
	}
	if i >= len(v) {
		return e.fatalf("string JSON called with incorrect number of arguments")
	}

	fail := func(format string, a ...any) error {
		msg := fmt.Sprintf(format, a...)
		if errVar == "" {
			return e.fatalf("string(JSON) %s", msg)
		}
		e.state.SetVar(errVar, msg)
		e.state.SetVar(out, msg)
		return nil
	}
	ok := func(value string) error {
		if errVar != "" {
			e.state.SetVar(errVar, "NOTFOUND")
		}
		e.state.SetVar(out, value)
		return nil
	}

	// The operation names itself first, then the document it applies to. Reading
	// this the other way round parses the JSON text as a keyword and produces a
	// baffling error, so the order is worth stating plainly.
	op := v[i]
	i++

	// EQUAL compares two documents and takes no member path.
	if op == "EQUAL" {
		if i+1 >= len(v) {
			return fail("EQUAL requires two JSON documents")
		}
		var a, b any
		if err := json.Unmarshal([]byte(v[i]), &a); err != nil {
			return fail("failed parsing json string: %v", err)
		}
		if err := json.Unmarshal([]byte(v[i+1]), &b); err != nil {
			return fail("failed parsing json string: %v", err)
		}
		return ok(boolVarOnOff(jsonEqual(a, b)))
	}

	if i >= len(v) {
		return fail("missing JSON document")
	}
	doc := v[i]
	i++
	path := v[i:]

	var root any
	if err := json.Unmarshal([]byte(doc), &root); err != nil {
		return fail("failed parsing json string: %v", err)
	}

	switch op {
	case "GET":
		node, err := jsonWalk(root, path)
		if err != nil {
			return fail("%v", err)
		}
		return ok(jsonScalar(node))

	case "TYPE":
		node, err := jsonWalk(root, path)
		if err != nil {
			return fail("%v", err)
		}
		return ok(jsonType(node))

	case "LENGTH":
		node, err := jsonWalk(root, path)
		if err != nil {
			return fail("%v", err)
		}
		switch n := node.(type) {
		case []any:
			return ok(strconv.Itoa(len(n)))
		case map[string]any:
			return ok(strconv.Itoa(len(n)))
		default:
			return fail("%s is not an array or object", strings.Join(path, "."))
		}

	case "MEMBER":
		if len(path) == 0 {
			return fail("MEMBER requires an index")
		}
		node, err := jsonWalk(root, path[:len(path)-1])
		if err != nil {
			return fail("%v", err)
		}
		obj, isObj := node.(map[string]any)
		if !isObj {
			return fail("%s is not an object", strings.Join(path[:len(path)-1], "."))
		}
		idx, err := strconv.Atoi(path[len(path)-1])
		if err != nil {
			return fail("MEMBER index %q is not a number", path[len(path)-1])
		}
		// Members are ordered by name so that MEMBER is deterministic; JSON
		// objects have no inherent order and CMake sorts them the same way.
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if idx < 0 || idx >= len(keys) {
			return fail("member index %d out of range", idx)
		}
		return ok(keys[idx])

	case "REMOVE":
		if len(path) == 0 {
			return fail("REMOVE requires a member path")
		}
		if err := jsonRemove(root, path); err != nil {
			return fail("%v", err)
		}
		return ok(jsonEncode(root))

	case "SET":
		if len(path) < 2 {
			return fail("SET requires a member path and a value")
		}
		var value any
		if err := json.Unmarshal([]byte(path[len(path)-1]), &value); err != nil {
			return fail("failed parsing json string: %v", err)
		}
		newRoot, err := jsonSet(root, path[:len(path)-1], value)
		if err != nil {
			return fail("%v", err)
		}
		return ok(jsonEncode(newRoot))
	}
	return fail("does not recognize sub-command %s", op)
}

// jsonWalk follows a member path into a document. An integer step indexes an
// array; anything else names an object member.
func jsonWalk(node any, path []string) (any, error) {
	for i, step := range path {
		switch n := node.(type) {
		case map[string]any:
			v, ok := n[step]
			if !ok {
				return nil, fmt.Errorf("member %q not found", strings.Join(path[:i+1], "."))
			}
			node = v
		case []any:
			idx, err := strconv.Atoi(step)
			if err != nil {
				return nil, fmt.Errorf("index %q is not a number", step)
			}
			if idx < 0 || idx >= len(n) {
				return nil, fmt.Errorf("index %d out of range", idx)
			}
			node = n[idx]
		default:
			return nil, fmt.Errorf("member %q is not an object or array", strings.Join(path[:i], "."))
		}
	}
	return node, nil
}

func jsonSet(root any, path []string, value any) (any, error) {
	if len(path) == 0 {
		return value, nil
	}
	parent, err := jsonWalk(root, path[:len(path)-1])
	if err != nil {
		return nil, err
	}
	last := path[len(path)-1]
	switch n := parent.(type) {
	case map[string]any:
		n[last] = value
		return root, nil
	case []any:
		idx, err := strconv.Atoi(last)
		if err != nil {
			return nil, fmt.Errorf("index %q is not a number", last)
		}
		if idx < 0 || idx >= len(n) {
			return nil, fmt.Errorf("index %d out of range", idx)
		}
		n[idx] = value
		return root, nil
	}
	return nil, fmt.Errorf("cannot set a member of a scalar")
}

func jsonRemove(root any, path []string) error {
	parent, err := jsonWalk(root, path[:len(path)-1])
	if err != nil {
		return err
	}
	last := path[len(path)-1]
	if obj, ok := parent.(map[string]any); ok {
		if _, exists := obj[last]; !exists {
			return fmt.Errorf("member %q not found", last)
		}
		delete(obj, last)
		return nil
	}
	// Removing from an array would have to rewrite the parent's reference to
	// it, which the walk above does not give access to.
	return fmt.Errorf("REMOVE from an array is not supported")
}

// jsonScalar renders a JSON value the way CMake's GET does: scalars become
// their text, and containers become their re-encoded JSON.
func jsonScalar(node any) string {
	switch n := node.(type) {
	case nil:
		return ""
	case bool:
		return boolVarOnOff(n)
	case string:
		return n
	case float64:
		// An integral value prints without a decimal point, so that an index
		// read out of a document can be fed straight back into math().
		if n == float64(int64(n)) {
			return strconv.FormatInt(int64(n), 10)
		}
		return strconv.FormatFloat(n, 'g', -1, 64)
	default:
		return jsonEncode(node)
	}
}

func jsonType(node any) string {
	switch node.(type) {
	case nil:
		return "NULL"
	case bool:
		return "BOOLEAN"
	case float64:
		return "NUMBER"
	case string:
		return "STRING"
	case []any:
		return "ARRAY"
	case map[string]any:
		return "OBJECT"
	}
	return "NULL"
}

func jsonEncode(node any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(node); err != nil {
		return ""
	}
	return strings.TrimRight(buf.String(), "\n")
}

func jsonEqual(a, b any) bool {
	return jsonEncode(a) == jsonEncode(b)
}

// boolVarOnOff renders a bool as CMake's ON/OFF, which is what string(JSON)
// yields for a boolean member rather than the 1/empty form used elsewhere.
func boolVarOnOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}
