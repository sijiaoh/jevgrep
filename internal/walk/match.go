package walk

import (
	"path"
	"strings"
)

// pattern is one gitignore-style pattern, compiled. The same type serves
// .gitignore files and -g globs: the two syntaxes are the same one, and a
// second matcher would be a second set of corner cases to get wrong.
type pattern struct {
	// negate is the leading "!". In an ignore file it re-includes a path; in a
	// -g glob it excludes one. Both are "this pattern says the opposite", and
	// which opposite is the caller's business.
	negate bool
	// dirOnly is the trailing "/": the pattern matches directories only.
	dirOnly bool
	// segs is the pattern split on "/", with "**" standing for any run of
	// segments. A pattern without a "/" of its own is stored with a leading
	// "**", which is exactly gitignore's "matches at any depth".
	segs []string
}

// parsePattern compiles one pattern. It reports false for a pattern with
// nothing left to match. Blank lines and "#" comments are the ignore file
// reader's business, not this function's: -g takes patterns that are neither.
func parsePattern(line string) (pattern, bool) {
	var p pattern
	if strings.HasPrefix(line, "!") {
		p.negate = true
		line = line[1:]
	}
	// The only escape gitignore has that changes what a pattern means here: a
	// leading "\" quotes the "!" or "#" that follows it.
	line = strings.TrimPrefix(line, `\`)
	if strings.HasSuffix(line, "/") {
		p.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	if line == "" {
		return pattern{}, false
	}

	// A "/" anywhere but at the end anchors the pattern to the directory the
	// ignore file sits in (or, for -g, to the search root).
	anchored := strings.Contains(line, "/")
	p.segs = strings.Split(strings.TrimPrefix(line, "/"), "/")
	if !anchored {
		p.segs = append([]string{"**"}, p.segs...)
	}
	return p, true
}

// match reports whether p covers rel, a slash-separated path relative to
// whatever the pattern is anchored to.
func (p pattern) match(rel string, isDir bool) bool {
	if p.dirOnly && !isDir {
		return false
	}
	return matchSegs(p.segs, strings.Split(rel, "/"))
}

// matchSegs matches a whole path against a whole pattern, segment by segment.
// Within a segment the syntax is path.Match's, which is gitignore's "*", "?"
// and "[...]" and, like gitignore, never matches across a "/".
func matchSegs(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// Trailing "**" is git's "everything inside", so it needs something
			// to be inside of: "a/**" covers a/b but not a itself.
			if len(pat) == 1 {
				return len(name) > 0
			}
			for i := range len(name) + 1 {
				if matchSegs(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		// A malformed class such as "[a" matches nothing rather than failing the
		// run: one bad line in a .gitignore is not worth refusing to search.
		if ok, err := path.Match(pat[0], name[0]); err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}
