package eval

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func init() {
	register("file", cmdFile)
}

// cmdFile dispatches the file() command. Only the sub-commands that a
// configure run can honour without network access or a process are handled
// here; DOWNLOAD and UPLOAD are refused rather than silently succeeding,
// because a project that gets an empty file and no error is worse off than one
// that gets a clear message.
func cmdFile(ctx context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("file called with incorrect number of arguments")
	}
	switch v[0] {
	case "READ":
		return fileRead(e, v)
	case "STRINGS":
		return fileStrings(e, v)
	case "WRITE":
		return fileWrite(e, v, false)
	case "APPEND":
		return fileWrite(e, v, true)
	case "TOUCH", "TOUCH_NOCREATE":
		return fileTouch(e, v)
	case "GLOB":
		return fileGlob(e, v, false)
	case "GLOB_RECURSE":
		return fileGlob(e, v, true)
	case "MAKE_DIRECTORY":
		for _, d := range v[1:] {
			if err := e.fs.MkdirAll(e.state.absPath(d)); err != nil {
				return e.fatalf("file MAKE_DIRECTORY failed on %s: %v", d, err)
			}
		}
		return nil
	case "REMOVE", "REMOVE_RECURSE":
		for _, p := range v[1:] {
			_ = e.fs.Remove(e.state.absPath(p))
		}
		return nil
	case "RENAME":
		return e.fatalf("file RENAME is not supported by this implementation")
	case "COPY", "INSTALL":
		return fileCopy(e, v)
	case "RELATIVE_PATH":
		if len(v) < 4 {
			return e.fatalf("file RELATIVE_PATH called with incorrect number of arguments")
		}
		rel, err := filepath.Rel(v[2], v[3])
		if err != nil {
			e.state.SetVar(v[1], v[3])
			return nil
		}
		e.state.SetVar(v[1], slashPath(rel))
		return nil
	case "TO_CMAKE_PATH":
		if len(v) < 3 {
			return e.fatalf("file TO_CMAKE_PATH called with incorrect number of arguments")
		}
		sep := ":"
		if isWindows() {
			sep = ";"
		}
		parts := strings.Split(v[1], sep)
		for i := range parts {
			parts[i] = slashPath(parts[i])
		}
		e.state.SetVar(v[2], JoinList(parts))
		return nil
	case "TO_NATIVE_PATH":
		if len(v) < 3 {
			return e.fatalf("file TO_NATIVE_PATH called with incorrect number of arguments")
		}
		p := v[1]
		if isWindows() {
			p = strings.ReplaceAll(p, "/", "\\")
		}
		e.state.SetVar(v[2], p)
		return nil
	case "REAL_PATH":
		if len(v) < 3 {
			return e.fatalf("file REAL_PATH called with incorrect number of arguments")
		}
		e.state.SetVar(v[2], slashPath(e.state.absPath(v[1])))
		return nil
	case "SIZE":
		if len(v) < 3 {
			return e.fatalf("file SIZE called with incorrect number of arguments")
		}
		fi, err := e.fs.Stat(e.state.absPath(v[1]))
		if err != nil {
			return e.fatalf("file SIZE cannot find %s", v[1])
		}
		e.state.SetVar(v[2], strconv.FormatInt(fi.Size(), 10))
		return nil
	case "MD5", "SHA1", "SHA224", "SHA256", "SHA384", "SHA512":
		if len(v) < 3 {
			return e.fatalf("file %s called with incorrect number of arguments", v[0])
		}
		data, err := e.fs.ReadFile(e.state.absPath(v[1]))
		if err != nil {
			return e.fatalf("file %s cannot read %s", v[0], v[1])
		}
		sum, err := hashBytes(v[0], data)
		if err != nil {
			return e.fatalf("%v", err)
		}
		e.state.SetVar(v[2], sum)
		return nil
	case "GENERATE":
		return fileGenerate(e, v)
	case "CONFIGURE":
		return fileConfigure(e, v)
	case "LOCK", "CREATE_LINK", "CHMOD", "CHMOD_RECURSE":
		return nil
	case "DOWNLOAD", "UPLOAD":
		return e.fatalf("file %s requires network access, which this implementation does not perform", v[0])
	case "GET_RUNTIME_DEPENDENCIES", "ARCHIVE_CREATE", "ARCHIVE_EXTRACT":
		return e.fatalf("file %s is not supported by this implementation", v[0])
	}
	return e.fatalf("file does not recognize sub-command %s", v[0])
}

func fileRead(e *evaluator, v []string) error {
	if len(v) < 3 {
		return e.fatalf("file READ called with incorrect number of arguments")
	}
	data, err := e.fs.ReadFile(e.state.absPath(v[1]))
	if err != nil {
		return e.fatalf("file READ failed to read %s", v[1])
	}
	offset, limit := 0, -1
	hexOut := false
	for i := 3; i < len(v); i++ {
		switch v[i] {
		case "OFFSET":
			if i+1 < len(v) {
				offset, _ = strconv.Atoi(v[i+1])
				i++
			}
		case "LIMIT":
			if i+1 < len(v) {
				limit, _ = strconv.Atoi(v[i+1])
				i++
			}
		case "HEX":
			hexOut = true
		}
	}
	if offset > len(data) {
		offset = len(data)
	}
	data = data[offset:]
	if limit >= 0 && limit < len(data) {
		data = data[:limit]
	}
	if hexOut {
		e.state.SetVar(v[2], hexString(data))
		return nil
	}
	e.state.SetVar(v[2], string(data))
	return nil
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0xf])
	}
	return string(out)
}

func fileStrings(e *evaluator, v []string) error {
	if len(v) < 3 {
		return e.fatalf("file STRINGS called with incorrect number of arguments")
	}
	data, err := e.fs.ReadFile(e.state.absPath(v[1]))
	if err != nil {
		return e.fatalf("file STRINGS failed to read %s", v[1])
	}

	limitCount, limitInput, limitOutput := -1, -1, -1
	lengthMin, lengthMax := 0, -1
	var regex *regexp.Regexp
	newlineConsume := false
	for i := 3; i < len(v); i++ {
		switch v[i] {
		case "LIMIT_COUNT":
			limitCount, _ = strconv.Atoi(next(v, i))
			i++
		case "LIMIT_INPUT":
			limitInput, _ = strconv.Atoi(next(v, i))
			i++
		case "LIMIT_OUTPUT":
			limitOutput, _ = strconv.Atoi(next(v, i))
			i++
		case "LENGTH_MINIMUM":
			lengthMin, _ = strconv.Atoi(next(v, i))
			i++
		case "LENGTH_MAXIMUM":
			lengthMax, _ = strconv.Atoi(next(v, i))
			i++
		case "REGEX":
			re, err := compileCMakeRegex(next(v, i))
			if err != nil {
				return e.fatalf("file STRINGS: %v", err)
			}
			regex = re
			i++
		case "NEWLINE_CONSUME":
			newlineConsume = true
		case "NO_HEX_CONVERSION", "ENCODING":
		}
	}
	if limitInput >= 0 && limitInput < len(data) {
		data = data[:limitInput]
	}

	// file STRINGS extracts printable runs, which is what makes it work on
	// binaries as well as text: anything unprintable terminates a string.
	var out []string
	var cur strings.Builder
	total := 0
	flush := func() {
		s := cur.String()
		cur.Reset()
		if len(s) < lengthMin {
			return
		}
		if lengthMax >= 0 && len(s) > lengthMax {
			s = s[:lengthMax]
		}
		if regex != nil && !regex.MatchString(s) {
			return
		}
		if limitCount >= 0 && len(out) >= limitCount {
			return
		}
		if limitOutput >= 0 && total+len(s) > limitOutput {
			return
		}
		total += len(s)
		out = append(out, s)
	}
	for _, c := range data {
		switch {
		case c == '\n' && newlineConsume:
			cur.WriteByte('\n')
		case c == '\n' || c == 0:
			flush()
		case c == '\r':
		case c >= 0x20 && c < 0x7f, c == '\t':
			cur.WriteByte(c)
		default:
			flush()
		}
	}
	// A blank line in the middle of the file is a real empty element, but the
	// newline that ends the last line is a terminator, not a separator, so it
	// must not produce a trailing empty one.
	if cur.Len() > 0 {
		flush()
	}
	e.state.SetVar(v[2], JoinList(out))
	return nil
}

func next(v []string, i int) string {
	if i+1 < len(v) {
		return v[i+1]
	}
	return ""
}

func fileWrite(e *evaluator, v []string, appendMode bool) error {
	if len(v) < 2 {
		return e.fatalf("file %s called with incorrect number of arguments", v[0])
	}
	target := e.state.absPath(v[1])
	content := strings.Join(v[2:], "")
	if err := e.fs.MkdirAll(dirOf(target)); err != nil {
		return e.fatalf("file %s could not create directory: %v", v[0], err)
	}
	if appendMode {
		if old, err := e.fs.ReadFile(target); err == nil {
			content = string(old) + content
		}
	}
	if err := e.fs.WriteFile(target, []byte(content)); err != nil {
		return e.fatalf("file %s failed to write %s: %v", v[0], v[1], err)
	}
	return nil
}

func fileTouch(e *evaluator, v []string) error {
	noCreate := v[0] == "TOUCH_NOCREATE"
	for _, p := range v[1:] {
		target := e.state.absPath(p)
		if _, err := e.fs.Stat(target); err != nil {
			if noCreate {
				continue
			}
			if err := e.fs.MkdirAll(dirOf(target)); err != nil {
				return e.fatalf("file TOUCH could not create directory: %v", err)
			}
			if err := e.fs.WriteFile(target, nil); err != nil {
				return e.fatalf("file TOUCH failed on %s: %v", p, err)
			}
		}
	}
	return nil
}

func fileGlob(e *evaluator, v []string, recurse bool) error {
	if len(v) < 2 {
		return e.fatalf("file GLOB called with incorrect number of arguments")
	}
	out := v[1]
	relativeTo := ""
	configureDepends := false
	listDirectories := true
	var patterns []string
	for i := 2; i < len(v); i++ {
		switch v[i] {
		case "RELATIVE":
			relativeTo = next(v, i)
			i++
		case "CONFIGURE_DEPENDS":
			configureDepends = true
		case "LIST_DIRECTORIES":
			listDirectories = isOn(next(v, i))
			i++
		case "FOLLOW_SYMLINKS":
		default:
			patterns = append(patterns, v[i])
		}
	}

	var matches []string
	for _, pat := range patterns {
		found, err := e.globPattern(pat, recurse, listDirectories)
		if err != nil {
			return e.fatalf("file GLOB: %v", err)
		}
		matches = append(matches, found...)
	}
	sort.Strings(matches)
	matches = dedupeSorted(matches)

	if relativeTo != "" {
		for i, m := range matches {
			if rel, err := filepath.Rel(relativeTo, m); err == nil {
				matches[i] = slashPath(rel)
			}
		}
	}
	if configureDepends {
		// A CONFIGURE_DEPENDS glob makes the build re-run configure when the
		// matched set changes, so the patterns have to be remembered.
		e.state.GlobDepends = append(e.state.GlobDepends, patterns...)
	}
	e.state.SetVar(out, JoinList(matches))
	return nil
}

// globPattern expands one glob, walking subdirectories when recurse is set.
func (e *evaluator) globPattern(pat string, recurse, listDirectories bool) ([]string, error) {
	pat = slashPath(e.state.absPath(pat))
	if !recurse {
		found, err := e.fs.Glob(pat)
		if err != nil {
			return nil, err
		}
		if listDirectories {
			return found, nil
		}
		return e.filesOnly(found), nil
	}

	// GLOB_RECURSE splits the pattern into a fixed directory prefix and a
	// trailing pattern, then matches the pattern at every depth below it.
	dir, base := splitGlobPrefix(pat)
	var out []string
	var walk func(string, int) error
	walk = func(d string, depth int) error {
		if depth > 64 {
			return nil
		}
		found, err := e.fs.Glob(d + "/" + base)
		if err != nil {
			return err
		}
		if listDirectories {
			out = append(out, found...)
		} else {
			out = append(out, e.filesOnly(found)...)
		}
		children, err := e.fs.Glob(d + "/*")
		if err != nil {
			return err
		}
		for _, c := range children {
			if fi, err := e.fs.Stat(c); err == nil && fi.IsDir() {
				if err := walk(slashPath(c), depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(dir, 0); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *evaluator) filesOnly(paths []string) []string {
	out := paths[:0]
	for _, p := range paths {
		if fi, err := e.fs.Stat(p); err == nil && !fi.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// splitGlobPrefix separates the leading literal directories of a glob from the
// first component that contains a wildcard.
func splitGlobPrefix(pat string) (dir, rest string) {
	parts := strings.Split(pat, "/")
	i := 0
	for i < len(parts)-1 && !strings.ContainsAny(parts[i], "*?[") {
		i++
	}
	if i == 0 {
		return ".", pat
	}
	return strings.Join(parts[:i], "/"), strings.Join(parts[i:], "/")
}

func dedupeSorted(in []string) []string {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for _, v := range in[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

func fileCopy(e *evaluator, v []string) error {
	var files []string
	dest := ""
	i := 1
	for ; i < len(v); i++ {
		if v[i] == "DESTINATION" {
			dest = next(v, i)
			i++
			break
		}
		files = append(files, v[i])
	}
	if dest == "" {
		return e.fatalf("file %s requires a DESTINATION", v[0])
	}
	destDir := e.state.absPath(dest)
	if err := e.fs.MkdirAll(destDir); err != nil {
		return e.fatalf("file %s could not create %s: %v", v[0], dest, err)
	}
	for _, f := range files {
		src := e.state.absPath(f)
		fi, err := e.fs.Stat(src)
		if err != nil {
			return e.fatalf("file %s cannot find %s", v[0], f)
		}
		if fi.IsDir() {
			// Directory copies are not walked here; a project that needs one
			// is better served by install(DIRECTORY).
			continue
		}
		data, err := e.fs.ReadFile(src)
		if err != nil {
			return e.fatalf("file %s cannot read %s", v[0], f)
		}
		if err := e.fs.WriteFile(joinPath(destDir, baseName(src)), data); err != nil {
			return e.fatalf("file %s cannot write into %s: %v", v[0], dest, err)
		}
	}
	return nil
}

func fileGenerate(e *evaluator, v []string) error {
	// file(GENERATE OUTPUT o CONTENT c) is deferred to generate time because
	// its content may contain generator expressions that are not resolvable
	// until the target graph is complete.
	var output, content, input, condition string
	for i := 1; i < len(v); i++ {
		switch v[i] {
		case "OUTPUT":
			output = next(v, i)
			i++
		case "CONTENT":
			content = next(v, i)
			i++
		case "INPUT":
			input = next(v, i)
			i++
		case "CONDITION":
			condition = next(v, i)
			i++
		}
	}
	if output == "" {
		return e.fatalf("file GENERATE requires an OUTPUT")
	}
	e.state.GeneratedFiles = append(e.state.GeneratedFiles, GeneratedFile{
		Output:    output,
		Content:   content,
		Input:     input,
		Condition: condition,
		SourceDir: e.state.Dir().Source,
		BinaryDir: e.state.Dir().Binary,
	})
	return nil
}

func fileConfigure(e *evaluator, v []string) error {
	var output, content string
	atOnly, escapeQuotes := false, false
	for i := 1; i < len(v); i++ {
		switch v[i] {
		case "OUTPUT":
			output = next(v, i)
			i++
		case "CONTENT":
			content = next(v, i)
			i++
		case "@ONLY":
			atOnly = true
		case "ESCAPE_QUOTES":
			escapeQuotes = true
		}
	}
	if output == "" {
		return e.fatalf("file CONFIGURE requires an OUTPUT")
	}
	target := output
	if !isAbsolutePath(target) {
		target = joinPath(e.state.Dir().Binary, target)
	}
	if err := e.fs.MkdirAll(dirOf(target)); err != nil {
		return e.fatalf("file CONFIGURE could not create directory: %v", err)
	}
	out := configureString(e.state, content, atOnly, escapeQuotes)
	if err := e.fs.WriteFile(target, []byte(out)); err != nil {
		return e.fatalf("file CONFIGURE could not write %s: %v", output, err)
	}
	return nil
}

// osFileMode is retained so that a future permissions implementation has a
// single place to convert CMake's permission names.
var _ = os.FileMode(0)
