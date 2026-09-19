package walk

import "testing"

func TestPatternMatch(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		isDir   bool
		want    bool
	}{
		// A pattern without a slash matches the name at any depth.
		{pattern: "a.txt", path: "a.txt", want: true},
		{pattern: "a.txt", path: "sub/a.txt", want: true},
		{pattern: "a.txt", path: "a/b/a.txt", want: true},
		{pattern: "a.txt", path: "a.txt.bak"},
		{pattern: "*.log", path: "sub/x.log", want: true},
		{pattern: "x?.log", path: "x1.log", want: true},
		{pattern: "x[0-9].log", path: "x1.log", want: true},
		{pattern: "x[0-9].log", path: "xa.log"},

		// A "*" stops at a separator, the way a shell glob does.
		{pattern: "sub/*", path: "sub/a.txt", want: true},
		{pattern: "sub/*", path: "sub/a/b.txt"},

		// A slash anchors the pattern where it was written.
		{pattern: "/a.txt", path: "a.txt", want: true},
		{pattern: "/a.txt", path: "sub/a.txt"},
		{pattern: "sub/a.txt", path: "sub/a.txt", want: true},
		{pattern: "sub/a.txt", path: "x/sub/a.txt"},

		// "**" spans any run of segments, and a trailing one needs something to
		// be inside of.
		{pattern: "a/**/c.txt", path: "a/c.txt", want: true},
		{pattern: "a/**/c.txt", path: "a/b/c.txt", want: true},
		{pattern: "a/**/c.txt", path: "a/b/x/c.txt", want: true},
		{pattern: "a/**", path: "a/b.txt", want: true},
		{pattern: "a/**", path: "a", isDir: true},
		{pattern: "**/a.txt", path: "x/a.txt", want: true},

		// A trailing slash means directories only.
		{pattern: "build/", path: "build", isDir: true, want: true},
		{pattern: "build/", path: "build"},

		// A malformed class matches nothing instead of failing the run.
		{pattern: "[a", path: "[a"},
	}

	for _, tt := range tests {
		p, ok := parsePattern(tt.pattern)
		if !ok {
			t.Errorf("parsePattern(%q) rejected it", tt.pattern)
			continue
		}
		if got := p.match(tt.path, tt.isDir); got != tt.want {
			t.Errorf("%q.match(%q, isDir=%v) = %v, want %v", tt.pattern, tt.path, tt.isDir, got, tt.want)
		}
	}
}

func TestParsePatternRejectsWhatHasNothingToMatch(t *testing.T) {
	for _, line := range []string{"", "!", "/", "!/"} {
		if _, ok := parsePattern(line); ok {
			t.Errorf("parsePattern(%q) accepted it", line)
		}
	}
}

// "!" and "#" are ordinary first characters once a backslash quotes them, which
// is the only gitignore escape that changes what a pattern covers.
func TestParsePatternUnquotesALeadingBackslash(t *testing.T) {
	p, ok := parsePattern(`\#a.txt`)
	if !ok {
		t.Fatal("rejected")
	}
	if p.negate {
		t.Error("negate set")
	}
	if !p.match("#a.txt", false) {
		t.Error(`\#a.txt does not match #a.txt`)
	}
}

func TestGlobSetSkip(t *testing.T) {
	tests := []struct {
		name  string
		globs []string
		path  string
		isDir bool
		want  bool
	}{
		{name: "no glob searches everything", path: "a.go"},
		{name: "an include leaves out what it misses", globs: []string{"*.go"}, path: "a.txt", want: true},
		{name: "an include keeps what it hits", globs: []string{"*.go"}, path: "a.go"},
		{name: "an exclude alone leaves the rest alone", globs: []string{"!*.go"}, path: "a.txt"},
		{name: "an exclude takes its own away", globs: []string{"!*.go"}, path: "a.go", want: true},
		{name: "the last to match decides", globs: []string{"!*.go", "a.go"}, path: "a.go"},
		{name: "and the other way round", globs: []string{"a.go", "!*.go"}, path: "a.go", want: true},

		// An include says nothing about directories, or the walk would prune
		// the tree the include was written to search.
		{name: "an include does not prune a directory", globs: []string{"*.go"}, path: "sub", isDir: true},
		{name: "an exclude does prune one", globs: []string{"!sub"}, path: "sub", isDir: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compileGlobs(tt.globs).skip(tt.path, tt.isDir); got != tt.want {
				t.Errorf("skip(%q, isDir=%v) = %v, want %v", tt.path, tt.isDir, got, tt.want)
			}
		})
	}
}

func TestIsSecret(t *testing.T) {
	tests := []struct {
		name   string
		secret bool
	}{
		{name: ".env", secret: true},
		{name: ".env.local", secret: true},
		{name: ".env.production", secret: true},
		{name: "server.pem", secret: true},
		{name: "tls.key", secret: true},
		{name: "cert.p12", secret: true},
		{name: "cert.pfx", secret: true},
		// ssh-keygen's own names, extensionless like every private key it
		// writes -- including the ones a user renamed to say what they are for.
		{name: "id_rsa", secret: true},
		{name: "id_dsa", secret: true},
		{name: "id_ecdsa", secret: true},
		{name: "id_ed25519", secret: true},
		{name: "id_ed25519_work", secret: true},
		// The public half is not a secret, and an "id_" file with an extension
		// is in practice source code: neither is worth dropping in silence.
		{name: "id_ed25519.pub", secret: false},
		{name: "id_generator.go", secret: false},
		{name: "id_map.rs", secret: false},
		{name: "env", secret: false},
		{name: "environment.go", secret: false},
		{name: "keys.go", secret: false},
		{name: "main.go", secret: false},
		{name: "README.md", secret: false},
		{name: "ID_RSA", secret: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSecret(tt.name); got != tt.secret {
				t.Errorf("isSecret(%q) = %v, want %v", tt.name, got, tt.secret)
			}
		})
	}
}
