package eval

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func init() {
	register("string", cmdString)
}

// cmdString dispatches the string() command, which is really eighteen commands
// wearing one name.
func cmdString(ctx context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) == 0 {
		return e.fatalf("string called with incorrect number of arguments")
	}
	switch vals[0] {
	case "FIND":
		return strFind(e, vals)
	case "REPLACE":
		return strReplace(e, vals)
	case "REGEX":
		return strRegex(e, vals)
	case "APPEND":
		return strAppend(e, vals)
	case "PREPEND":
		return strPrepend(e, vals)
	case "CONCAT":
		return strConcat(e, vals)
	case "JOIN":
		return strJoin(e, vals)
	case "TOLOWER":
		return strCase(e, vals, strings.ToLower)
	case "TOUPPER":
		return strCase(e, vals, strings.ToUpper)
	case "LENGTH":
		return strLength(e, vals)
	case "SUBSTRING":
		return strSubstring(e, vals)
	case "STRIP":
		return strStrip(e, vals)
	case "GENEX_STRIP":
		return strGenexStrip(e, vals)
	case "REPEAT":
		return strRepeat(e, vals)
	case "COMPARE":
		return strCompare(e, vals)
	case "ASCII":
		return strASCII(e, vals)
	case "HEX":
		return strHex(e, vals)
	case "MD5", "SHA1", "SHA224", "SHA256", "SHA384", "SHA512":
		return strHash(e, vals)
	case "MAKE_C_IDENTIFIER":
		return strMakeCIdentifier(e, vals)
	case "RANDOM":
		return strRandom(e, vals)
	case "TIMESTAMP":
		return strTimestamp(e, vals)
	case "UUID":
		return strUUID(e, vals)
	case "CONFIGURE":
		return strConfigure(ctx, e, vals)
	case "JSON":
		return strJSON(e, vals)
	}
	return e.fatalf("string does not recognize sub-command %s", vals[0])
}

func strFind(e *evaluator, v []string) error {
	if len(v) < 4 {
		return e.fatalf("string FIND called with incorrect number of arguments")
	}
	haystack, needle, out := v[1], v[2], v[3]
	reverse := containsStr(v[4:], "REVERSE")
	idx := strings.Index(haystack, needle)
	if reverse {
		idx = strings.LastIndex(haystack, needle)
	}
	e.state.SetVar(out, strconv.Itoa(idx))
	return nil
}

func strReplace(e *evaluator, v []string) error {
	if len(v) < 4 {
		return e.fatalf("string REPLACE called with incorrect number of arguments")
	}
	match, with, out := v[1], v[2], v[3]
	// The input is every remaining argument concatenated, not just the fourth:
	// string(REPLACE a b OUT ${LIST}) joins the list first.
	input := strings.Join(v[4:], "")
	e.state.SetVar(out, strings.ReplaceAll(input, match, with))
	return nil
}

func strRegex(e *evaluator, v []string) error {
	if len(v) < 2 {
		return e.fatalf("string REGEX called with incorrect number of arguments")
	}
	switch v[1] {
	case "MATCH":
		if len(v) < 4 {
			return e.fatalf("string REGEX MATCH called with incorrect number of arguments")
		}
		re, err := compileCMakeRegex(v[2])
		if err != nil {
			return e.fatalf("string REGEX MATCH: %v", err)
		}
		input := strings.Join(v[4:], "")
		m := re.FindStringSubmatch(input)
		if m == nil {
			e.state.SetVar(v[3], "")
			setMatchVars(e.state, nil)
			return nil
		}
		e.state.SetVar(v[3], m[0])
		setMatchVars(e.state, m)
		return nil

	case "MATCHALL":
		if len(v) < 4 {
			return e.fatalf("string REGEX MATCHALL called with incorrect number of arguments")
		}
		re, err := compileCMakeRegex(v[2])
		if err != nil {
			return e.fatalf("string REGEX MATCHALL: %v", err)
		}
		input := strings.Join(v[4:], "")
		all := re.FindAllStringSubmatch(input, -1)
		var out []string
		for _, m := range all {
			out = append(out, m[0])
		}
		if len(all) > 0 {
			setMatchVars(e.state, all[len(all)-1])
		} else {
			setMatchVars(e.state, nil)
		}
		e.state.SetVar(v[3], JoinList(out))
		return nil

	case "REPLACE":
		if len(v) < 5 {
			return e.fatalf("string REGEX REPLACE called with incorrect number of arguments")
		}
		re, err := compileCMakeRegex(v[2])
		if err != nil {
			return e.fatalf("string REGEX REPLACE: %v", err)
		}
		input := strings.Join(v[5:], "")
		repl, err := cmakeReplacement(v[3])
		if err != nil {
			return e.fatalf("string REGEX REPLACE: %v", err)
		}
		e.state.SetVar(v[4], re.ReplaceAllString(input, repl))
		return nil
	}
	return e.fatalf("string REGEX does not recognize sub-command %s", v[1])
}

// cmakeReplacement translates CMake's \1 backreference syntax into the ${1}
// form Go's regexp package expects, and escapes any literal $ so that it is not
// read as a Go template reference.
func cmakeReplacement(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '$':
			b.WriteString("$$")
		case s[i] == '\\' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9':
			b.WriteString("${")
			b.WriteByte(s[i+1])
			b.WriteString("}")
			i++
		case s[i] == '\\' && i+1 < len(s):
			b.WriteByte(s[i+1])
			i++
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String(), nil
}

// setMatchVars publishes CMAKE_MATCH_<n> after a regex operation.
func setMatchVars(s *State, m []string) {
	for i := 0; i < 10; i++ {
		name := "CMAKE_MATCH_" + strconv.Itoa(i)
		if i < len(m) {
			s.SetVar(name, m[i])
		} else {
			s.UnsetVar(name)
		}
	}
	if len(m) == 0 {
		s.SetVar("CMAKE_MATCH_COUNT", "0")
	} else {
		s.SetVar("CMAKE_MATCH_COUNT", strconv.Itoa(len(m)-1))
	}
}

func strAppend(e *evaluator, v []string) error {
	if len(v) < 2 {
		return e.fatalf("string APPEND called with incorrect number of arguments")
	}
	e.state.SetVar(v[1], e.state.GetVar(v[1])+strings.Join(v[2:], ""))
	return nil
}

func strPrepend(e *evaluator, v []string) error {
	if len(v) < 2 {
		return e.fatalf("string PREPEND called with incorrect number of arguments")
	}
	e.state.SetVar(v[1], strings.Join(v[2:], "")+e.state.GetVar(v[1]))
	return nil
}

func strConcat(e *evaluator, v []string) error {
	if len(v) < 2 {
		return e.fatalf("string CONCAT called with incorrect number of arguments")
	}
	e.state.SetVar(v[1], strings.Join(v[2:], ""))
	return nil
}

func strJoin(e *evaluator, v []string) error {
	if len(v) < 3 {
		return e.fatalf("string JOIN called with incorrect number of arguments")
	}
	e.state.SetVar(v[2], strings.Join(v[3:], v[1]))
	return nil
}

func strCase(e *evaluator, v []string, f func(string) string) error {
	if len(v) < 3 {
		return e.fatalf("string %s called with incorrect number of arguments", v[0])
	}
	e.state.SetVar(v[2], f(v[1]))
	return nil
}

func strLength(e *evaluator, v []string) error {
	if len(v) < 3 {
		return e.fatalf("string LENGTH called with incorrect number of arguments")
	}
	e.state.SetVar(v[2], strconv.Itoa(len(v[1])))
	return nil
}

func strSubstring(e *evaluator, v []string) error {
	if len(v) < 5 {
		return e.fatalf("string SUBSTRING called with incorrect number of arguments")
	}
	s := v[1]
	begin, err1 := strconv.Atoi(v[2])
	length, err2 := strconv.Atoi(v[3])
	if err1 != nil || err2 != nil {
		return e.fatalf("string SUBSTRING given non-numeric offset or length")
	}
	if begin < 0 || begin > len(s) {
		return e.fatalf("string SUBSTRING begin index: %d is out of range 0 - %d", begin, len(s))
	}
	// A length of -1 means "to the end", which is how callers ask for a suffix
	// without first computing the string's length.
	end := len(s)
	if length >= 0 && begin+length < end {
		end = begin + length
	}
	e.state.SetVar(v[4], s[begin:end])
	return nil
}

func strStrip(e *evaluator, v []string) error {
	if len(v) < 3 {
		return e.fatalf("string STRIP called with incorrect number of arguments")
	}
	e.state.SetVar(v[2], strings.TrimSpace(v[1]))
	return nil
}

var genexPattern = regexp.MustCompile(`\$<[^<>]*>`)

func strGenexStrip(e *evaluator, v []string) error {
	if len(v) < 3 {
		return e.fatalf("string GENEX_STRIP called with incorrect number of arguments")
	}
	s := v[1]
	// Strip innermost-first so that nested generator expressions all go.
	for {
		next := genexPattern.ReplaceAllString(s, "")
		if next == s {
			break
		}
		s = next
	}
	e.state.SetVar(v[2], s)
	return nil
}

func strRepeat(e *evaluator, v []string) error {
	if len(v) < 4 {
		return e.fatalf("string REPEAT called with incorrect number of arguments")
	}
	n, err := strconv.Atoi(v[2])
	if err != nil || n < 0 {
		return e.fatalf("string REPEAT given invalid count %q", v[2])
	}
	e.state.SetVar(v[3], strings.Repeat(v[1], n))
	return nil
}

func strCompare(e *evaluator, v []string) error {
	if len(v) < 5 {
		return e.fatalf("string COMPARE called with incorrect number of arguments")
	}
	a, b, out := v[2], v[3], v[4]
	var r bool
	switch v[1] {
	case "LESS":
		r = a < b
	case "GREATER":
		r = a > b
	case "EQUAL":
		r = a == b
	case "NOTEQUAL":
		r = a != b
	case "LESS_EQUAL":
		r = a <= b
	case "GREATER_EQUAL":
		r = a >= b
	default:
		return e.fatalf("string COMPARE does not recognize mode %s", v[1])
	}
	e.state.SetVar(out, boolVar(r))
	return nil
}

func strASCII(e *evaluator, v []string) error {
	if len(v) < 3 {
		return e.fatalf("string ASCII called with incorrect number of arguments")
	}
	out := v[len(v)-1]
	var b strings.Builder
	for _, code := range v[1 : len(v)-1] {
		n, err := strconv.Atoi(code)
		if err != nil || n < 0 || n > 255 {
			return e.fatalf("string ASCII given invalid character code %q", code)
		}
		b.WriteByte(byte(n))
	}
	e.state.SetVar(out, b.String())
	return nil
}

func strHex(e *evaluator, v []string) error {
	if len(v) < 3 {
		return e.fatalf("string HEX called with incorrect number of arguments")
	}
	e.state.SetVar(v[2], hex.EncodeToString([]byte(v[1])))
	return nil
}

func strHash(e *evaluator, v []string) error {
	if len(v) < 3 {
		return e.fatalf("string %s called with incorrect number of arguments", v[0])
	}
	sum, err := hashBytes(v[0], []byte(v[2]))
	if err != nil {
		return e.fatalf("%v", err)
	}
	e.state.SetVar(v[1], sum)
	return nil
}

// hashBytes computes one of the digests CMake exposes, returned lowercase hex.
func hashBytes(algo string, data []byte) (string, error) {
	switch algo {
	case "MD5":
		s := md5.Sum(data)
		return hex.EncodeToString(s[:]), nil
	case "SHA1":
		s := sha1.Sum(data)
		return hex.EncodeToString(s[:]), nil
	case "SHA224":
		s := sha256.Sum224(data)
		return hex.EncodeToString(s[:]), nil
	case "SHA256":
		s := sha256.Sum256(data)
		return hex.EncodeToString(s[:]), nil
	case "SHA384":
		s := sha512.Sum384(data)
		return hex.EncodeToString(s[:]), nil
	case "SHA512":
		s := sha512.Sum512(data)
		return hex.EncodeToString(s[:]), nil
	}
	return "", fmt.Errorf("unknown hash algorithm %s", algo)
}

func strMakeCIdentifier(e *evaluator, v []string) error {
	if len(v) < 3 {
		return e.fatalf("string MAKE_C_IDENTIFIER called with incorrect number of arguments")
	}
	e.state.SetVar(v[2], makeCIdentifier(v[1]))
	return nil
}

// makeCIdentifier converts a string into a valid C identifier: every character
// that cannot appear in one becomes an underscore, and a leading digit gets an
// underscore in front of it.
func makeCIdentifier(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out != "" && out[0] >= '0' && out[0] <= '9' {
		return "_" + out
	}
	return out
}

func strRandom(e *evaluator, v []string) error {
	length := 5
	alphabet := "qwertyuiopasdfghjklzxcvbnmQWERTYUIOPASDFGHJKLZXCVBNM0123456789"
	var seed int64 = -1
	i := 1
	for ; i+1 < len(v); i += 2 {
		switch v[i] {
		case "LENGTH":
			n, err := strconv.Atoi(v[i+1])
			if err != nil {
				return e.fatalf("string RANDOM given invalid LENGTH %q", v[i+1])
			}
			length = n
		case "ALPHABET":
			alphabet = v[i+1]
		case "RANDOM_SEED":
			n, err := strconv.ParseInt(v[i+1], 10, 64)
			if err != nil {
				return e.fatalf("string RANDOM given invalid RANDOM_SEED %q", v[i+1])
			}
			seed = n
		default:
			goto done
		}
	}
done:
	if i >= len(v) {
		return e.fatalf("string RANDOM called with incorrect number of arguments")
	}
	out := v[len(v)-1]
	src := rand.New(rand.NewSource(time.Now().UnixNano()))
	if seed >= 0 {
		src = rand.New(rand.NewSource(seed))
	}
	if alphabet == "" {
		e.state.SetVar(out, "")
		return nil
	}
	b := make([]byte, length)
	for j := range b {
		b[j] = alphabet[src.Intn(len(alphabet))]
	}
	e.state.SetVar(out, string(b))
	return nil
}

func strTimestamp(e *evaluator, v []string) error {
	if len(v) < 2 {
		return e.fatalf("string TIMESTAMP called with incorrect number of arguments")
	}
	out := v[1]
	format := "%Y-%m-%dT%H:%M:%S"
	if len(v) > 2 && v[2] != "UTC" {
		format = v[2]
	}
	now := time.Now()
	if containsStr(v, "UTC") {
		now = now.UTC()
	}
	// SOURCE_DATE_EPOCH makes the timestamp reproducible, which is the whole
	// point of honouring it: a build that embeds a timestamp is not otherwise
	// bit-for-bit repeatable.
	if epoch := e.state.Env["SOURCE_DATE_EPOCH"]; epoch != "" {
		if n, err := strconv.ParseInt(epoch, 10, 64); err == nil {
			now = time.Unix(n, 0).UTC()
		}
	}
	e.state.SetVar(out, formatTimestamp(now, format))
	return nil
}

// formatTimestamp renders the strftime-style specifiers CMake documents.
func formatTimestamp(t time.Time, format string) string {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 >= len(format) {
			b.WriteByte(format[i])
			continue
		}
		i++
		switch format[i] {
		case 'Y':
			fmt.Fprintf(&b, "%04d", t.Year())
		case 'y':
			fmt.Fprintf(&b, "%02d", t.Year()%100)
		case 'm':
			fmt.Fprintf(&b, "%02d", int(t.Month()))
		case 'd':
			fmt.Fprintf(&b, "%02d", t.Day())
		case 'H':
			fmt.Fprintf(&b, "%02d", t.Hour())
		case 'M':
			fmt.Fprintf(&b, "%02d", t.Minute())
		case 'S':
			fmt.Fprintf(&b, "%02d", t.Second())
		case 'j':
			fmt.Fprintf(&b, "%03d", t.YearDay())
		case 'b':
			b.WriteString(t.Format("Jan"))
		case 'B':
			b.WriteString(t.Format("January"))
		case 'a':
			b.WriteString(t.Format("Mon"))
		case 'A':
			b.WriteString(t.Format("Monday"))
		case 's':
			fmt.Fprintf(&b, "%d", t.Unix())
		case 'U':
			fmt.Fprintf(&b, "%02d", (t.YearDay()+6-int(t.Weekday()))/7)
		case 'V':
			_, w := t.ISOWeek()
			fmt.Fprintf(&b, "%02d", w)
		case 'z':
			b.WriteString(t.Format("-0700"))
		case 'Z':
			b.WriteString(t.Format("MST"))
		case '%':
			b.WriteByte('%')
		default:
			b.WriteByte('%')
			b.WriteByte(format[i])
		}
	}
	return b.String()
}

func strUUID(e *evaluator, v []string) error {
	if len(v) < 2 {
		return e.fatalf("string UUID called with incorrect number of arguments")
	}
	out := v[1]
	var namespace, name string
	upper := false
	for i := 2; i < len(v); i++ {
		switch v[i] {
		case "NAMESPACE":
			if i+1 < len(v) {
				namespace = v[i+1]
				i++
			}
		case "NAME":
			if i+1 < len(v) {
				name = v[i+1]
				i++
			}
		case "UPPER":
			upper = true
		}
	}
	// Only the deterministic MD5 (version 3) form is produced: a UUID that
	// changes on every configure would invalidate the cache on every run.
	ns := parseUUIDBytes(namespace)
	h := md5.New()
	h.Write(ns)
	h.Write([]byte(name))
	sum := h.Sum(nil)
	sum[6] = (sum[6] & 0x0f) | 0x30
	sum[8] = (sum[8] & 0x3f) | 0x80
	id := fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(sum[0:4]), hex.EncodeToString(sum[4:6]),
		hex.EncodeToString(sum[6:8]), hex.EncodeToString(sum[8:10]),
		hex.EncodeToString(sum[10:16]))
	if upper {
		id = strings.ToUpper(id)
	}
	e.state.SetVar(out, id)
	return nil
}

func parseUUIDBytes(s string) []byte {
	clean := strings.ReplaceAll(s, "-", "")
	b, err := hex.DecodeString(clean)
	if err != nil {
		return []byte(s)
	}
	return b
}

func strConfigure(ctx context.Context, e *evaluator, v []string) error {
	if len(v) < 3 {
		return e.fatalf("string CONFIGURE called with incorrect number of arguments")
	}
	atOnly := containsStr(v[3:], "@ONLY")
	escapeQuotes := containsStr(v[3:], "ESCAPE_QUOTES")
	e.state.SetVar(v[2], configureString(e.state, v[1], atOnly, escapeQuotes))
	return nil
}
