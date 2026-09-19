// Package walk expands a directory into the files jevgrep should search.
package walk

import (
	"io/fs"
	"iter"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// gitDir is skipped always, and neither --hidden nor --no-ignore opens it.
// ripgrep is laxer here, and jevgrep cannot afford to be: every line searched
// is billed, a repository's object store dwarfs its working tree, and not one
// line of it is a line a user meant to read -- the content is already there as
// files.
const gitDir = ".git"

// sshDir holds private keys whatever its contents are named, so it is treated
// as one of them.
const sshDir = ".ssh"

// secretNames never leave the machine through a walk, and no option opens them:
// -g is a filter, not a key, and `-g '.env*'` will not bring .env back. Sending
// a credential to a remote API cannot be undone, so the only way to search one
// is to name that file on the command line, which is a thing a user can only do
// on purpose.
var secretNames = []string{".env", ".env.*", "*.pem", "*.key", "*.p12", "*.pfx"}

// sshKeyPrefix is how ssh-keygen names what it writes: id_rsa, id_ed25519,
// id_ed25519_work. Matching "id_*" alone would also swallow source files --
// id_generator.go, id_map.rs -- and a grep that silently drops a file it was
// asked about costs more trust than a key costs money. What tells the two
// apart is the extension: a private key has none, its public half is ".pub",
// and source code always has one.
const sshKeyPrefix = "id_"

// Options are the command line's answers to "which of these files are worth
// searching".
type Options struct {
	// Globs are the -g patterns in command line order; a "!" prefix turns one
	// into a skip rule.
	Globs []string
	// Hidden is --hidden: also search the entries whose name starts with ".".
	Hidden bool
	// NoIgnore is --no-ignore: do not read .gitignore, .ignore or
	// .git/info/exclude.
	NoIgnore bool
}

// Files walks root and yields the files under it worth searching, in the order
// they should be searched: each directory's entries by name, files and
// subdirectories in the one sequence. The order is part of the contract, not an
// accident of the filesystem -- search prints in input order, so this is the
// output order, and a golden test needs the same command to produce the same
// output twice.
//
// A directory that cannot be read is yielded as an empty path with a non-nil
// error and the walk goes on: the rest of the tree is still worth searching,
// and the caller turns that error into a message and exit code 2. This is why
// the sequence carries errors at all rather than being a plain iter.Seq.
//
// Symbolic links found during the walk are not followed, so there is no cycle
// to detect. A link named on the command line is followed, which is where the
// caller resolved root. Devices, FIFOs and sockets are skipped.
func Files(root string, opts Options) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		w := walker{opts: opts, globs: compileGlobs(opts.Globs), yield: yield}

		// The repository's own exclude file goes in first, under the root's
		// .gitignore that dir is about to push: it is the weakest of them.
		var stack ignoreStack
		if !opts.NoIgnore {
			if patterns := readIgnoreFile(filepath.Join(root, gitExcludePath)); len(patterns) > 0 {
				stack = append(stack, ignoreFile{patterns: patterns})
			}
		}
		w.dir(root, "", stack)
	}
}

type walker struct {
	opts  Options
	globs globSet
	yield func(string, error) bool
}

// dir walks one directory and reports whether the consumer wants more.
func (w *walker) dir(dir, rel string, stack ignoreStack) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return w.yield("", err)
	}
	if !w.opts.NoIgnore {
		stack = stack.push(dir, rel)
	}

	for _, e := range entries {
		name := e.Name()
		childRel := name
		if rel != "" {
			childRel = rel + "/" + name
		}
		child := joinPath(dir, name)

		switch {
		case e.Type()&fs.ModeSymlink != 0:
			continue
		case e.IsDir():
			if w.skipDir(name, childRel, stack) {
				continue
			}
			if !w.dir(child, childRel, stack) {
				return false
			}
		case e.Type().IsRegular():
			if w.skipFile(name, childRel, stack) {
				continue
			}
			// Last, because it is the only rule that has to open the file.
			// Walked binaries are skipped in silence: saying so once per file
			// would bury the matches on any real repository.
			switch binary, err := IsBinary(child); {
			case err != nil:
				if !w.yield("", err) {
					return false
				}
			case !binary:
				if !w.yield(child, nil) {
					return false
				}
			}
		}
	}
	return true
}

// skipDir and skipFile apply the filters in order, first one to fire wins. The
// order is what makes ".git is never searched" and "a secret is never searched"
// true regardless of the other options: neither --hidden nor --no-ignore nor -g
// is consulted before those two have had their say.
func (w *walker) skipDir(name, rel string, stack ignoreStack) bool {
	switch {
	case name == gitDir, name == sshDir:
		return true
	case !w.opts.Hidden && isHidden(name):
		return true
	case !w.opts.NoIgnore && stack.skip(rel, true):
		return true
	}
	return w.globs.skip(rel, true)
}

func (w *walker) skipFile(name, rel string, stack ignoreStack) bool {
	switch {
	case !w.opts.Hidden && isHidden(name):
		return true
	case !w.opts.NoIgnore && stack.skip(rel, false):
		return true
	case w.globs.skip(rel, false):
		return true
	}
	return isSecret(name)
}

// joinPath puts name under dir the way grep prints it: the operand keeps the
// spelling the user gave it, so that "./src" yields "./src/a.go" and ".." yields
// "../a.go" -- a path in the output is a path they can paste back. filepath.Join
// would clean both down to something they never typed.
//
// A root of exactly "." is the one exception, and contributes no prefix at all:
// it is what jevgrep searches when -r was given no PATH, and "./" on every line
// of a whole-tree search is noise on every line. grep prints the prefix when the
// "." was typed and drops it when it was implied; jevgrep drops it either way,
// because the two spell the same search.
func joinPath(dir, name string) string {
	for len(dir) > 0 && os.IsPathSeparator(dir[len(dir)-1]) {
		dir = dir[:len(dir)-1]
	}
	switch dir {
	case ".":
		return name
	// The operand was the filesystem root, or all separators: its entries hang
	// straight off it.
	case "":
		return string(filepath.Separator) + name
	}
	return dir + string(filepath.Separator) + name
}

func isHidden(name string) bool { return strings.HasPrefix(name, ".") }

func isSecret(name string) bool {
	for _, p := range secretNames {
		// Case sensitive, like the filesystems these names come from: "ID_RSA"
		// is not a thing ssh-keygen writes.
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return strings.HasPrefix(name, sshKeyPrefix) && path.Ext(name) == ""
}
