package walk

import (
	"bytes"
	"errors"
	"io"
	"os"
)

// sniffBytes is how much of a file's head decides whether it is binary. Text
// that starts with 8 KiB of clean bytes and turns into a blob later is rare
// enough not to pay for reading whole files to catch.
const sniffBytes = 8192

// IsBinary reports whether path holds a NUL byte in its first 8 KiB, which is
// how grep and every tool like it tells a blob from text.
//
// It is exported because the rule outlives the walk: a path named on the
// command line skips every "which files" filter, but not this one. Those
// filters answer "did the user mean this file"; this one answers "is there a
// line in it to send", and sending an executable line by line spends money to
// ship the binary form of whatever secrets it was built with.
func IsBinary(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, sniffBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, err
	}
	return bytes.IndexByte(buf[:n], 0) >= 0, nil
}
