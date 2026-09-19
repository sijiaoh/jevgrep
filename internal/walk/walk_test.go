package walk_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sijiaoh/jevgrep/internal/walk"
)

// tree writes a fixture tree and returns its root. A value is the file's
// content; the directories on the way are created as needed.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// collect walks root and returns the paths relative to it, slash separated, so
// that a test can be written the way the tree was.
func collect(t *testing.T, root string, opts walk.Options) ([]string, []error) {
	t.Helper()

	var (
		found []string
		errs  []error
	)
	for path, err := range walk.Files(root, opts) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			t.Fatal(relErr)
		}
		found = append(found, filepath.ToSlash(rel))
	}
	return found, errs
}

// base is the fixture most cases start from: a mix of plain files, a
// subdirectory, hidden entries, an ignore file and a secret.
var base = map[string]string{
	"x.txt":        "a1\n",
	"sub/y.txt":    "a2\n",
	"sub/y.log":    "noise\n",
	".dot.txt":     "a4\n",
	".hide/z.txt":  "a5\n",
	".gitignore":   "*.log\n",
	".env":         "TOKEN=1\n",
	".git/objects": "blob\n",
}

func TestFiles(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		opts  walk.Options
		want  []string
	}{
		{
			name:  "names in order, hidden and ignored and secret left out",
			files: base,
			want:  []string{"sub/y.txt", "x.txt"},
		},
		{
			name:  "--hidden adds the dotted entries but never .git or a secret",
			files: base,
			opts:  walk.Options{Hidden: true},
			want:  []string{".dot.txt", ".gitignore", ".hide/z.txt", "sub/y.txt", "x.txt"},
		},
		{
			name:  "--no-ignore stops obeying .gitignore",
			files: base,
			opts:  walk.Options{NoIgnore: true},
			want:  []string{"sub/y.log", "sub/y.txt", "x.txt"},
		},
		{
			name:  "--hidden --no-ignore together still refuse .git and the secret",
			files: base,
			opts:  walk.Options{Hidden: true, NoIgnore: true},
			want:  []string{".dot.txt", ".gitignore", ".hide/z.txt", "sub/y.log", "sub/y.txt", "x.txt"},
		},
		{
			name: "an ignored directory is not descended into",
			files: map[string]string{
				".gitignore":    "build/\n",
				"build/big.txt": "a\n",
				"keep.txt":      "a\n",
			},
			want: []string{"keep.txt"},
		},
		{
			name: "a nested .gitignore overrides the one above it",
			files: map[string]string{
				".gitignore":     "*.txt\n",
				"a.txt":          "a\n",
				"sub/.gitignore": "!keep.txt\n",
				"sub/keep.txt":   "a\n",
				"sub/drop.txt":   "a\n",
			},
			want: []string{"sub/keep.txt"},
		},
		{
			name: "the last matching pattern in one file wins",
			files: map[string]string{
				".gitignore": "*.txt\n!keep.txt\n",
				"keep.txt":   "a\n",
				"drop.txt":   "a\n",
			},
			want: []string{"keep.txt"},
		},
		{
			name: ".ignore has the last word over .gitignore",
			files: map[string]string{
				".gitignore": "a.txt\n",
				".ignore":    "!a.txt\nb.txt\n",
				"a.txt":      "a\n",
				"b.txt":      "a\n",
				"c.txt":      "a\n",
			},
			want: []string{"a.txt", "c.txt"},
		},
		{
			name: ".git/info/exclude is obeyed too",
			files: map[string]string{
				".git/info/exclude": "a.txt\n",
				"a.txt":             "a\n",
				"b.txt":             "a\n",
			},
			want: []string{"b.txt"},
		},
		{
			name: "an anchored pattern only matches at its own level",
			files: map[string]string{
				".gitignore": "/a.txt\n",
				"a.txt":      "a\n",
				"sub/a.txt":  "a\n",
			},
			want: []string{"sub/a.txt"},
		},
		{
			name: "** spans any number of directories",
			files: map[string]string{
				".gitignore":       "sub/**/drop.txt\n",
				"sub/drop.txt":     "a\n",
				"sub/a/b/drop.txt": "a\n",
				"drop.txt":         "a\n",
			},
			want: []string{"drop.txt"},
		},
		{
			name: "an include glob searches only what it matches",
			files: map[string]string{
				"a.go":     "a\n",
				"b.txt":    "a\n",
				"sub/c.go": "a\n",
			},
			opts: walk.Options{Globs: []string{"*.go"}},
			want: []string{"a.go", "sub/c.go"},
		},
		{
			name: "an exclude glob takes away from everything else",
			files: map[string]string{
				"a.go":     "a\n",
				"b.txt":    "a\n",
				"sub/c.go": "a\n",
			},
			opts: walk.Options{Globs: []string{"!*.go"}},
			want: []string{"b.txt"},
		},
		{
			name: "the last glob to match decides",
			files: map[string]string{
				"a.go":     "a\n",
				"sub/b.go": "a\n",
			},
			opts: walk.Options{Globs: []string{"*.go", "!sub/**"}},
			want: []string{"a.go"},
		},
		{
			name: "a glob with a slash is anchored to the search root",
			files: map[string]string{
				"sub/a.go":     "a\n",
				"other/a.go":   "a\n",
				"sub/dir/b.go": "a\n",
			},
			opts: walk.Options{Globs: []string{"sub/**"}},
			want: []string{"sub/a.go", "sub/dir/b.go"},
		},
		{
			name: "a glob is a filter, not a key to the secrets",
			files: map[string]string{
				".env":       "TOKEN=1\n",
				"id_rsa":     "key\n",
				"deploy.pem": "key\n",
				"app.log":    "a\n",
			},
			opts: walk.Options{Hidden: true, Globs: []string{".env*", "id_*", "*.pem", "*.log"}},
			want: []string{"app.log"},
		},
		{
			name: "nothing under .ssh is searched",
			files: map[string]string{
				".ssh/config": "Host x\n",
				"a.txt":       "a\n",
			},
			opts: walk.Options{Hidden: true, NoIgnore: true},
			want: []string{"a.txt"},
		},
		{
			name: "a file with a NUL byte in its head is not searched",
			files: map[string]string{
				"blob.bin": "ELF\x00\x01\x02",
				"a.txt":    "a\n",
			},
			want: []string{"a.txt"},
		},
		{
			name: "a NUL past the first 8 KiB is not looked for",
			files: map[string]string{
				"late.txt": strings.Repeat("x", 9000) + "\x00",
			},
			want: []string{"late.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := tree(t, tt.files)
			found, errs := collect(t, root, tt.opts)
			if len(errs) != 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
			if !slices.Equal(found, tt.want) {
				t.Errorf("walked\n%v\nwant\n%v", found, tt.want)
			}
		})
	}
}

// An empty PATH would be one more thing every caller has to guard against, and
// a tree with nothing to search is not an error.
func TestEmptyTreeYieldsNothing(t *testing.T) {
	found, errs := collect(t, tree(t, nil), walk.Options{})
	if len(found) != 0 || len(errs) != 0 {
		t.Errorf("got %v, %v; want nothing", found, errs)
	}
}

func TestSymlinksAreNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege this test cannot assume on Windows")
	}

	root := tree(t, map[string]string{"real/a.txt": "a\n"})
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	found, errs := collect(t, root, walk.Options{})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	// Once through "real", and no second pass through the link: the walk that
	// followed links would need cycle detection, and this is why it does not.
	if !slices.Equal(found, []string{"real/a.txt"}) {
		t.Errorf("walked %v, want [real/a.txt]", found)
	}
}

// A directory that cannot be read has to reach the caller, because it is what
// turns the run's exit code into 2; the rest of the tree is still searched.
func TestUnreadableDirectoryIsReportedAndTheWalkGoesOn(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix permissions that stop the user running the test")
	}

	root := tree(t, map[string]string{
		"a/locked.txt": "a\n",
		"b/open.txt":   "a\n",
	})
	locked := filepath.Join(root, "a")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	found, errs := collect(t, root, walk.Options{})
	if len(errs) != 1 {
		t.Fatalf("got %d errors, want 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), locked) {
		t.Errorf("error %q does not name %q", errs[0], locked)
	}
	if !slices.Equal(found, []string{"b/open.txt"}) {
		t.Errorf("walked %v, want [b/open.txt]", found)
	}
}

// The consumer stopping early is the ordinary case: search stops reading once
// the run is cancelled, and a walk that kept opening files after that would
// keep paying for them.
func TestStoppingEarlyEndsTheWalk(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "a\n", "b.txt": "a\n", "c.txt": "a\n"})

	var found []string
	for path, err := range walk.Files(root, walk.Options{}) {
		if err != nil {
			t.Fatal(err)
		}
		found = append(found, filepath.Base(path))
		if len(found) == 2 {
			break
		}
	}
	if !slices.Equal(found, []string{"a.txt", "b.txt"}) {
		t.Errorf("walked %v, want [a.txt b.txt]", found)
	}
}

func TestIsBinary(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "text", content: "hello\nworld\n"},
		{name: "empty", content: ""},
		{name: "NUL in the head", content: "\x7fELF\x00\x01", want: true},
		{name: "NUL just inside the window", content: strings.Repeat("x", 8191) + "\x00", want: true},
		{name: "NUL just outside it", content: strings.Repeat("x", 8192) + "\x00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(tree(t, map[string]string{"f": tt.content}), "f")
			got, err := walk.IsBinary(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("IsBinary = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsBinaryReportsAMissingFile(t *testing.T) {
	if _, err := walk.IsBinary(filepath.Join(t.TempDir(), "gone")); err == nil {
		t.Error("IsBinary on a missing file returned no error")
	}
}

// The operand is echoed back the way it was typed, because a path in the output
// is a path the reader pastes into the next command.
func TestPathsKeepTheSpellingOfTheRoot(t *testing.T) {
	root := tree(t, map[string]string{"d/a.txt": "a\n"})
	t.Chdir(root)

	// The separator jevgrep joins with is the platform's; what a root brings
	// along is whatever the caller typed.
	sep := string(filepath.Separator)
	tests := []struct {
		root string
		want string
	}{
		{root: "d", want: "d" + sep + "a.txt"},
		{root: "d/", want: "d" + sep + "a.txt"},
		{root: "./d", want: "./d" + sep + "a.txt"},
		// "." is the root -r searches when it was given no PATH, and a "./" on
		// every line of a whole-tree search is noise on every line.
		{root: ".", want: "d" + sep + "a.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.root, func(t *testing.T) {
			var found []string
			for path, err := range walk.Files(tt.root, walk.Options{}) {
				if err != nil {
					t.Fatal(err)
				}
				found = append(found, path)
			}
			if !slices.Equal(found, []string{tt.want}) {
				t.Errorf("walked %v, want [%s]", found, tt.want)
			}
		})
	}
}
