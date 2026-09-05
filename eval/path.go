package eval

import (
	"path"
	"strings"
)

// CMake speaks in forward slashes on every platform. A path that reaches a
// CMakeLists.txt with backslashes in it is not merely ugly: the backslash is
// the language's escape character, so `C:\build\ninja` loses its \b and \n the
// next time it passes through argument expansion. Every path this package
// stores or hands to a script therefore goes through these functions rather
// than through path/filepath, whose separator follows the host.

// slashPath converts a path to CMake's form.
func slashPath(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

// joinPath joins path elements with forward slashes and cleans the result.
func joinPath(elem ...string) string {
	parts := make([]string, 0, len(elem))
	for _, e := range elem {
		if e != "" {
			parts = append(parts, slashPath(e))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	joined := path.Join(parts...)
	// path.Join strips a Windows drive's trailing slash context; re-attach the
	// root when the first element carried one.
	if root := rootName(parts[0]); root != "" && !strings.HasPrefix(joined, root) {
		return root + joined
	}
	return joined
}

// relPath returns target expressed relative to base, or "" if it is not below
// it. Unlike filepath.Rel it never produces a backslash and never walks up: a
// caller that gets "" knows the path is outside the tree and can say so.
func relPath(base, target string) string {
	base = strings.TrimSuffix(slashPath(base), "/")
	target = slashPath(target)
	if base == "" {
		return target
	}
	if !strings.EqualFold(target, base) && !hasPathPrefix(target, base) {
		return ""
	}
	rel := strings.TrimPrefix(target[len(base):], "/")
	return rel
}

// hasPathPrefix reports whether target lies under base, comparing whole path
// components so that "/ab" is not treated as being under "/a".
func hasPathPrefix(target, base string) bool {
	if len(target) <= len(base) {
		return false
	}
	if target[len(base)] != '/' {
		return false
	}
	// Path comparison is case-insensitive on Windows, where "C:/Foo" and
	// "c:/foo" name the same directory.
	if isWindows() {
		return strings.EqualFold(target[:len(base)], base)
	}
	return target[:len(base)] == base
}

// BaseName returns the final component of a path.
func BaseName(p string) string {
	p = slashPath(p)
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
