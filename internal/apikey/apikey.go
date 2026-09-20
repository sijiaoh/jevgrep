// Package apikey resolves the TypeSafe API key jevgrep authenticates with, and
// implements --login, the one command that writes a key down.
//
// Nothing here ever puts a key in an error, a message or a log line: §6 of the
// plan keeps credentials out of everything the user or a terminal scrollback
// can see, and a wrapped error is the easiest place to lose that by accident.
package apikey

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvVar is the environment variable read before the key file. Its name is
// TypeSafe's own, so that a key already exported for their SDKs works here
// without being copied anywhere.
const EnvVar = "TYPESAFE_API_KEY"

// SignupURL is where a user without a key gets one. The CLI shows it in the
// "no API key" hint; it lives here so the hint and --login cannot drift.
const SignupURL = "https://console.typesafe.ai/keys"

const (
	// configHomeEnvVar is the XDG variable. It is honored everywhere, so it is
	// read here rather than left to os.UserConfigDir, which only consults it
	// on Unix.
	configHomeEnvVar = "XDG_CONFIG_HOME"

	configSubdir = "jevgrep"
	keyFileName  = "api_key"

	// The key file is readable by its owner only, and the directory keeps
	// other users from listing it. chmod is a no-op on Windows; that is not an
	// error and not worth warning about, because the file lands under the
	// user's own AppData there.
	keyFileMode = 0o600
	keyDirMode  = 0o700
)

// ErrNotFound reports that neither source held a key. The CLI turns it into the
// three-line hint that names EnvVar and SignupURL.
var ErrNotFound = errors.New("apikey: no API key found")

// Load returns the API key, preferring the environment over the stored file so
// that a key exported for one command never loses to a stale saved one.
//
// It returns ErrNotFound when there is no key anywhere. A key file that exists
// but cannot be read is a different failure and is returned as such: silently
// falling through to "no key" would send the user to --login to write a file
// that is already there.
func Load() (string, error) {
	if key := strings.TrimSpace(os.Getenv(EnvVar)); key != "" {
		return key, nil
	}

	path, err := Path()
	if err != nil {
		return "", err
	}

	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", ErrNotFound
	case err != nil:
		// Wrapped, not reformatted: the CLI reports file errors as
		// "jevgrep: PATH: <os error>" and needs the *fs.PathError underneath.
		return "", fmt.Errorf("apikey: read key file: %w", err)
	}

	key := strings.TrimSpace(string(b))
	if key == "" {
		return "", ErrNotFound
	}
	return key, nil
}

// Path returns the absolute path of the key file, whether or not it exists.
//
// XDG_CONFIG_HOME wins on every platform when it is set, not just on Unix:
// someone who has pointed their config at another directory means it, and
// os.UserConfigDir ignores the variable outside Unix.
func Path() (string, error) {
	dir := os.Getenv(configHomeEnvVar)
	switch {
	// Rejected rather than resolved against the working directory, which is
	// both what os.UserConfigDir does with it and the only way --login can
	// promise the absolute path it prints. A key file whose location depends
	// on where jevgrep was run from would go missing on the next run.
	case dir != "" && !filepath.IsAbs(dir):
		return "", fmt.Errorf("apikey: %s is relative, want an absolute path", configHomeEnvVar)
	case dir == "":
		var err error
		dir, err = os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("apikey: locate config directory: %w", err)
		}
	}
	return filepath.Join(dir, configSubdir, keyFileName), nil
}

// Save writes key to the key file, creating the directory as needed, and
// returns the path written. An existing key is replaced without asking: a user
// running --login is there to change the key.
func Save(key string) (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, keyDirMode); err != nil {
		return "", fmt.Errorf("apikey: create config directory: %w", err)
	}

	// Written and renamed into place so that a crash or a full disk cannot
	// leave half a key behind, which would read back as a key that simply does
	// not work.
	tmp, err := os.CreateTemp(dir, keyFileName+"-*")
	if err != nil {
		return "", fmt.Errorf("apikey: create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Harmless once the rename succeeded, and the one thing that keeps a
		// readable key out of the config directory when it did not.
		_ = os.Remove(tmpName)
	}()

	if err := writeKeyFile(tmp, key); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("apikey: install key file: %w", err)
	}
	return path, nil
}

// writeKeyFile fills an already-created temporary file with the key and closes
// it. The permissions are narrowed before the key is written, never after:
// os.CreateTemp already makes the file 0600, and chmod here only reasserts it
// against a umask that a future refactor might let through.
func writeKeyFile(f *os.File, key string) error {
	defer func() { _ = f.Close() }()

	if err := f.Chmod(keyFileMode); err != nil {
		return fmt.Errorf("apikey: set key file permissions: %w", err)
	}
	// One trailing newline and nothing else, so the file is what `cat` and any
	// other tool expect a single-value config file to be.
	if _, err := f.WriteString(key + "\n"); err != nil {
		return fmt.Errorf("apikey: write key file: %w", err)
	}
	// Flushed to disk before the rename: the rename is what makes the file
	// visible, and a visible file with unwritten contents is the empty-key
	// failure this whole dance exists to prevent.
	if err := f.Sync(); err != nil {
		return fmt.Errorf("apikey: flush key file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("apikey: close key file: %w", err)
	}
	return nil
}
