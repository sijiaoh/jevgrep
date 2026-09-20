package cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// newCache returns a cache in a directory of its own, so that no test can read
// or overwrite the cache of whoever is running it.
func newCache(t *testing.T, model string) *Cache {
	t.Helper()

	t.Setenv(cacheHomeEnvVar, t.TempDir())
	c, err := Open(model)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAnAnsweredLineIsRememberedAcrossRuns(t *testing.T) {
	c := newCache(t, "jev-1")
	c.Store("a disk error", []string{"write failed", "all good"}, []float64{0.9, 0.1})

	// A second Cache over the same directory is the next run: nothing is
	// shared with the first but the files.
	next, err := Open("jev-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		want  float64
	}{{"write failed", 0.9}, {"all good", 0.1}} {
		got, ok := next.Lookup("a disk error", tc.query)
		if !ok || got != tc.want {
			t.Errorf("Lookup(%q) = %v, %v; want %v, true", tc.query, got, ok, tc.want)
		}
	}
}

func TestOnlyTheSameQuestionHits(t *testing.T) {
	c := newCache(t, "jev-1")
	c.Store("a disk error", []string{"write failed"}, []float64{0.9})

	if _, ok := c.Lookup("a disk error", "write failed "); ok {
		t.Error("a different line hit the cache")
	}
	if _, ok := c.Lookup("A disk error", "write failed"); ok {
		t.Error("a differently spelled meaning hit the cache; the model tells them apart, so the cache must too")
	}

	// The model is part of the key: --model pins the calibration, and a score
	// from another model is not that calibration.
	other := &Cache{dir: c.dir, model: "jev-2", now: c.now}
	if _, ok := other.Lookup("a disk error", "write failed"); ok {
		t.Error("another model hit the cache")
	}
}

func TestAnswersExpire(t *testing.T) {
	c := newCache(t, "jev-1")
	c.Store("a disk error", []string{"write failed"}, []float64{0.9})

	next := &Cache{dir: c.dir, model: c.model, now: func() time.Time {
		return time.Now().Add((ttlDays + 1) * 24 * time.Hour)
	}}
	if _, ok := next.Lookup("a disk error", "write failed"); ok {
		t.Errorf("an answer older than %d days was still believed", ttlDays)
	}
}

func TestNothingSearchableIsWrittenDown(t *testing.T) {
	const (
		meaning = "a password in the source"
		line    = "const token = \"hunter2\""
	)
	c := newCache(t, "jev-1")
	c.Store(meaning, []string{line}, []float64{0.9})

	for _, name := range files(t, c.dir) {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{meaning, line, "hunter2", c.model} {
			if bytes.Contains(b, []byte(secret)) {
				t.Errorf("%s holds %q; the cache stores hashes only", filepath.Base(name), secret)
			}
		}
	}
}

func TestTheCacheIsReadableByItsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	c := newCache(t, "jev-1")
	c.Store("a disk error", []string{"write failed"}, []float64{0.9})

	info, err := os.Stat(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != cacheDirMode {
		t.Errorf("directory mode = %v, want %v", got, fs.FileMode(cacheDirMode))
	}
	for _, name := range files(t, c.dir) {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != cacheFileMode {
			t.Errorf("%s mode = %v, want %v", filepath.Base(name), got, fs.FileMode(cacheFileMode))
		}
	}
}

func TestAHalfWrittenRecordIsIgnoredAndTheRestStands(t *testing.T) {
	c := newCache(t, "jev-1")
	c.Store("a disk error", []string{"write failed"}, []float64{0.9})

	// What two processes appending at once can leave behind.
	name := files(t, c.dir)[0]
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_APPEND, cacheFileMode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(make([]byte, recordSize/2)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	next, err := Open("jev-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := next.Lookup("a disk error", "write failed"); !ok {
		t.Error("a torn record at the end of the file lost the whole shard")
	}
}

func TestAnOverfullShardIsEmptiedRatherThanGrowing(t *testing.T) {
	c := newCache(t, "jev-1")
	shard := shardOf(c.key("a disk error", "write failed"))
	if err := os.WriteFile(c.path(shard), make([]byte, maxShardBytes), cacheFileMode); err != nil {
		t.Fatal(err)
	}

	c.Store("a disk error", []string{"write failed"}, []float64{0.9})

	info, err := os.Stat(c.path(shard))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != recordSize {
		t.Errorf("shard size = %d, want %d: a full shard is thrown away, not appended to", info.Size(), recordSize)
	}
}

func TestTwoProcessesCanWriteAtOnce(t *testing.T) {
	c := newCache(t, "jev-1")
	// A second Cache over the same directory is a second jevgrep: nothing but
	// the files is shared, and nothing locks them.
	other, err := Open("jev-1")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i, writer := range []*Cache{c, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 200 {
				writer.Store("a disk error", []string{fmt.Sprintf("line %d-%d", i, j)}, []float64{0.9})
			}
		}()
	}
	wg.Wait()

	next, err := Open("jev-1")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		for j := range 200 {
			query := fmt.Sprintf("line %d-%d", i, j)
			if _, ok := next.Lookup("a disk error", query); !ok {
				t.Fatalf("Lookup(%q) missed: an append was lost", query)
			}
		}
	}
}

func TestTheCacheHomeVariableWinsOnEveryPlatform(t *testing.T) {
	home := t.TempDir()
	t.Setenv(cacheHomeEnvVar, home)

	got, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, cacheSubdir); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
}

func TestARelativeCacheHomeIsRefused(t *testing.T) {
	t.Setenv(cacheHomeEnvVar, "relative/cache")

	if _, err := Open("jev-1"); err == nil {
		t.Errorf("a relative %s was accepted; the cache would move with the working directory", cacheHomeEnvVar)
	}
}

// fakeScorer answers every line with the same score and counts what it was
// asked.
type fakeScorer struct {
	asked []string
	err   error
}

func (f *fakeScorer) Score(_ context.Context, _ string, lines []string) ([]float64, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.asked = append(f.asked, lines...)
	scores := make([]float64, len(lines))
	for i := range scores {
		scores[i] = 0.7
	}
	return scores, nil
}

func TestWrapFilesAwayWhatTheModelAnswered(t *testing.T) {
	c := newCache(t, "jev-1")
	next := c.Wrap(&fakeScorer{})

	if _, err := next.Score(context.Background(), "a disk error", []string{"write failed"}); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.Lookup("a disk error", "write failed"); !ok || got != 0.7 {
		t.Errorf("Lookup = %v, %v; want 0.7, true", got, ok)
	}
}

func TestAFailedBatchIsNotRemembered(t *testing.T) {
	c := newCache(t, "jev-1")
	next := c.Wrap(&fakeScorer{err: errors.New("boom")})

	if _, err := next.Score(context.Background(), "a disk error", []string{"write failed"}); err == nil {
		t.Fatal("Score returned no error")
	}
	if _, ok := c.Lookup("a disk error", "write failed"); ok {
		t.Error("a failed batch was cached; one outage would become a month of wrong results")
	}
}

func files(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "scores-") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	if len(out) == 0 {
		t.Fatal("no cache files were written")
	}
	return out
}

func TestNothingIsWrittenOnceTheCacheIsClosed(t *testing.T) {
	c := newCache(t, "jev-1")
	c.Close()

	// A batch that was still in flight when the run ended, answering now --
	// and answering successfully, with a context nobody has cancelled. What
	// makes this safe is the shut door, not anything the batch knows about
	// the run it belonged to: a context check here would be a race, since
	// whatever it decided the store would still land afterwards.
	if _, err := c.Wrap(&fakeScorer{}).Score(t.Context(), "a disk error", []string{"write failed"}); err != nil {
		t.Fatal(err)
	}
	c.Close() // closing twice is not an error

	entries, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing may appear on disk after the run is over: nobody waits for the
	// batches still in flight, so a file written here is a file written after
	// jevgrep said it was done.
	if len(entries) != 0 {
		t.Errorf("the cache directory holds %d files, want none", len(entries))
	}

	// And what was already known is still readable: Close ends writing, not
	// the cache.
	if _, ok := c.Lookup("a disk error", "write failed"); ok {
		t.Error("the dropped answer is being served from the cache")
	}
}

func TestAReadOnlyCacheCreatesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv(cacheHomeEnvVar, home)

	c, err := OpenForReading("jev-1")
	if err != nil {
		t.Fatal(err)
	}
	// Reading a cache that was never written is not an error, it is a miss.
	if _, ok := c.Lookup("a disk error", "write failed"); ok {
		t.Error("an empty cache answered a question")
	}
	// Nor may anything reach the disk through it: it is born closed so that
	// there is no second place where that has to be remembered.
	c.Store("a disk error", []string{"write failed"}, []float64{0.9})

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("%s holds %v, want a cache that reads to leave no trace", home, entries)
	}
}
