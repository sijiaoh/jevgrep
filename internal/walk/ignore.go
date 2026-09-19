package walk

import (
	"os"
	"path/filepath"
	"strings"
)

// ignoreNames are the per-directory ignore files, in rising precedence: .ignore
// is the one a user writes for search tools, so it gets the last word over the
// .gitignore that git shares with them.
var ignoreNames = []string{".gitignore", ".ignore"}

// gitExcludePath is git's repository-local ignore file. It is read once, at the
// search root, and is the weakest of them all. The global
// ~/.config/git/ignore is deliberately not read: it would make the same command
// return different results on two machines, and that is a bug report nobody can
// reproduce.
var gitExcludePath = filepath.Join(gitName, "info", "exclude")

// ignoreFile is one file's patterns together with the directory they are
// written against, as a slash-separated path relative to the search root.
type ignoreFile struct {
	base     string
	patterns []pattern
}

// ignoreStack is the ignore files covering the directory being walked, outermost
// first.
type ignoreStack []ignoreFile

// push returns the stack for a directory, reading whatever ignore files it
// holds. The result never shares storage with s: sibling directories are walked
// one after the other from the same parent stack.
func (s ignoreStack) push(dir, rel string) ignoreStack {
	out := s
	for _, name := range ignoreNames {
		patterns := readIgnoreFile(filepath.Join(dir, name))
		if len(patterns) == 0 {
			continue
		}
		out = append(out[:len(out):len(out)], ignoreFile{base: rel, patterns: patterns})
	}
	return out
}

// skip reports whether the ignore files exclude rel. The closest file that has
// anything to say about the path decides, and within one file the last matching
// pattern wins -- gitignore's rule, so that a "!" line can re-include what a
// line above it swept up.
func (s ignoreStack) skip(rel string, isDir bool) bool {
	for i := len(s) - 1; i >= 0; i-- {
		f := s[i]
		sub := rel
		if f.base != "" {
			sub = rel[len(f.base)+1:]
		}
		for j := len(f.patterns) - 1; j >= 0; j-- {
			if f.patterns[j].match(sub, isDir) {
				return !f.patterns[j].negate
			}
		}
	}
	return false
}

// readIgnoreFile compiles one ignore file. A file that is not there, or cannot
// be read, contributes nothing: an unreadable .gitignore is not a reason to
// stop searching, and reporting it would be noise on every repository that has
// none.
func readIgnoreFile(path string) []pattern {
	// Read whole rather than scanned: bufio.Scanner gives up silently on a line
	// over its buffer, and an ignore file truncated halfway is an ignore file
	// that quietly stops excluding things -- here, that is a bill.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var patterns []pattern
	for line := range strings.Lines(string(data)) {
		// Trailing blanks are not part of a pattern, which is what lets an
		// ignore file be edited without the editor's stray space changing it.
		line = strings.TrimRight(line, " \t\r\n")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if p, ok := parsePattern(line); ok {
			patterns = append(patterns, p)
		}
	}
	return patterns
}

// globSet is the -g patterns, in command line order.
type globSet struct {
	patterns []pattern
	// include records that at least one pattern was an include, which is what
	// turns the default from "search it" into "only these".
	include bool
}

func compileGlobs(globs []string) globSet {
	var set globSet
	for _, g := range globs {
		p, ok := parsePattern(g)
		if !ok {
			continue
		}
		set.patterns = append(set.patterns, p)
		set.include = set.include || !p.negate
	}
	return set
}

// skip reports whether -g filters rel out: the last pattern to match decides,
// and a path no pattern matched is searched only when no include pattern was
// given at all.
//
// A directory is only ever filtered out by an exclude pattern. An include like
// "*.go" matches no directory name, so honoring it on directories would prune
// the whole tree and find nothing -- the include has to be applied to the files
// it was written about.
func (g globSet) skip(rel string, isDir bool) bool {
	for i := len(g.patterns) - 1; i >= 0; i-- {
		p := g.patterns[i]
		if isDir && !p.negate {
			continue
		}
		if p.match(rel, isDir) {
			return p.negate
		}
	}
	if isDir {
		return false
	}
	return g.include
}
