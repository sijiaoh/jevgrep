// Package cache remembers the probability the model gave a line, so that
// searching the same thing twice only pays once.
//
// Nothing here stores a line, a meaning or a path: a record is a SHA-256 of
// what was asked plus the answer and the day it arrived, and that is the whole
// of it (§3.6, §6). The reason is that a cache is a file that outlives the run
// and that nobody thinks about again -- a grep of a private tree must not leave
// the tree behind in the user's home directory.
//
// One approximation is deliberate and worth knowing about: the API is asked
// about a whole chunk of lines at once, so a line's answer could in principle
// depend on which other lines shared its request, while this package treats
// the answer as a function of (line, meaning) alone. jevgrep already assumes
// that independence without a cache -- the same line falls in different
// batches on different runs -- and chunking is an implementation detail no
// option exposes (§4). --no-cache is the way out if it ever bites.
//
// It was measured rather than assumed: scoring the same lines alone, one per
// request, reversed, padded with unrelated lines and in a full batch of real
// source moved their probabilities by about 0.1 at worst (0.08-0.12 over
// repeated runs, and 0.01-0.04 on every line that was not a boundary case) --
// the same spread two identical requests already have, since the model
// quantises its answers to 0.01. No line crossed the default threshold. See
// internal/calibration (`make calibrate`), which re-measures it; §11 wants
// that re-run after a model update.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// cacheHomeEnvVar is the XDG variable, honored on every platform for the
	// same reason apikey honors XDG_CONFIG_HOME: someone who has pointed their
	// cache at another directory means it, and os.UserCacheDir only consults
	// the variable on Unix.
	cacheHomeEnvVar = "XDG_CACHE_HOME"

	cacheSubdir = "jevgrep"

	// Owner-only, like the key file, but for a different reason: a hash is not
	// a credential, yet it can be attacked with a dictionary of guessed lines,
	// and the directory listing alone says when this machine last searched.
	// chmod is a no-op on Windows, where the files land under the user's own
	// AppData.
	cacheFileMode = 0o600
	cacheDirMode  = 0o700
)

const (
	// formatVersion goes into every key, so that changing the record layout or
	// the way a key is built retires the old entries instead of misreading
	// them.
	formatVersion = 1

	// A record is the key, the probability and the day it was written.
	keySize    = sha256.Size
	recordSize = keySize + 8 + 4

	// ttlDays is how long an answer is believed. The model behind jev-latest
	// can move under us while the name in the key stays the same, and this is
	// the only thing that ever washes such an answer out. Thirty days rather
	// than a week because the cache is not a correctness mechanism -- anyone
	// who needs a fresh score has --no-cache, anyone who needs a fixed one has
	// --model -- so it should lean towards "the second search is free".
	ttlDays = 30

	// shards splits the records over a few files. It bounds what one lookup
	// reads, and it bounds what is lost when a file has to be thrown away.
	shards = 16

	// maxShardBytes caps one shard. A lookup loads the shard it touches into
	// memory and a search touches all of them, so this is really a memory
	// budget: 16 shards of 1 MiB hold around 380,000 answers and cost tens of
	// megabytes resident at worst.
	//
	// An overfull shard is emptied rather than pruned: the cache is a pure
	// speed-up, so losing entries costs one more paid search, and that is a
	// far better trade than the bookkeeping an exact eviction policy needs.
	maxShardBytes = 1 << 20

	// appendBatch caps how many records go into one write. Concurrent jevgrep
	// processes append to the same file without locking, which is safe only
	// while a write is small enough that the kernel does not split it.
	appendBatch = 64
)

// Cache is the answers this machine already has. It is safe for concurrent
// use: lookups happen on the goroutine reading the input while stores happen
// on the ones talking to the API.
type Cache struct {
	dir   string
	model string
	// now is a field so tests can age a record without waiting a month.
	now func() time.Time

	mu sync.Mutex
	// closed stops every further write. See Close.
	closed bool
	// loaded[i] holds shard i once it has been read, keyed by the record key.
	loaded [shards]map[[keySize]byte]float64
}

// Open returns the cache for model, creating its directory if it is not there.
//
// A cache that cannot be opened is not an error the user has to hear about:
// the caller drops it and searches without one. The error says which of the
// two happened; it is not something to fail the run over.
func Open(model string) (*Cache, error) {
	c, err := OpenForReading(model)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.dir, cacheDirMode); err != nil {
		return nil, fmt.Errorf("cache: create cache directory: %w", err)
	}
	c.closed = false
	return c, nil
}

// OpenForReading returns a cache that answers from whatever is already on disk
// and creates nothing: not a record, not the directory itself. It comes back
// closed, so a Store that reached it by mistake is dropped rather than
// trusted to be prevented somewhere else.
//
// It is what --dry-run needs. A command whose whole promise is that nothing
// was sent has no business leaving a directory behind in the user's home
// either, and a cache that is not there simply knows nothing -- which is the
// right answer, because a run that finds no cache pays the full price the dry
// run quoted.
func OpenForReading(model string) (*Cache, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	return &Cache{dir: dir, model: model, now: time.Now, closed: true}, nil
}

// Dir returns the absolute path of the cache directory, whether or not it
// exists.
func Dir() (string, error) {
	dir := os.Getenv(cacheHomeEnvVar)
	switch {
	// Rejected rather than resolved against the working directory, for the
	// same reason the key file's is: a cache whose location depends on where
	// jevgrep was run from is a cache that never hits.
	case dir != "" && !filepath.IsAbs(dir):
		return "", fmt.Errorf("cache: %s is relative, want an absolute path", cacheHomeEnvVar)
	case dir == "":
		var err error
		dir, err = os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("cache: locate cache directory: %w", err)
		}
	}
	return filepath.Join(dir, cacheSubdir), nil
}

// Lookup returns the probability this machine already has for asking meaning
// of query, and whether there is one. A hit is indistinguishable from a fresh
// answer everywhere downstream: it is the same number, reported the same way.
func (c *Cache) Lookup(meaning, query string) (float64, bool) {
	k := c.key(meaning, query)

	c.mu.Lock()
	defer c.mu.Unlock()

	score, ok := c.shard(k)[k]
	return score, ok
}

// Store remembers one answered batch. Only a batch the server answered gets
// here: caching "we do not know" would turn one outage into a month of wrong
// results.
//
// Failing to write is not reported. There is nothing the user would do about
// it and nothing wrong with the results; the next run simply pays again.
func (c *Cache) Store(meaning string, queries []string, scores []float64) {
	if len(queries) != len(scores) {
		return
	}
	day := c.today()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}

	var pending [shards][]byte
	for i, query := range queries {
		k := c.key(meaning, query)
		// An answer already on disk is not written twice: the file only ever
		// grows, so a re-run of the same search would double its size.
		known := c.shard(k)
		if _, ok := known[k]; ok {
			continue
		}
		known[k] = scores[i]
		pending[shardOf(k)] = append(pending[shardOf(k)], record(k, scores[i], day)...)
	}
	for i, data := range pending {
		if len(data) > 0 {
			c.append(i, data)
		}
	}
}

// Close ends the cache's writing life. Once it returns, this cache will not
// create or extend a file again, whatever is still in flight behind it: a
// batch that comes back later finds the door shut and drops its answer.
//
// That is the guarantee the caller needs to exit cleanly. Nothing waits for
// the requests still on the wire when a run ends -- -q stops at its first
// match, Ctrl-C stops everything -- so without this, a file could appear in
// the user's home directory seconds after jevgrep printed its last line and
// returned. It takes the same lock a store does, so a store already under way
// finishes first and Close never blocks on anything but that; it does not
// wait for the network.
//
// Reading (Lookup) still works afterwards, and closing twice is fine.
func (c *Cache) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

// Wrap returns a Scorer that files away everything next answers.
func (c *Cache) Wrap(next Scorer) Scorer { return &scorer{next: next, cache: c} }

// Scorer is the part of *jev.Client this package decorates, declared here so
// that the cache does not have to know about the scheduler that drives it.
type Scorer interface {
	Score(ctx context.Context, meaning string, lines []string) ([]float64, error)
}

type scorer struct {
	next  Scorer
	cache *Cache
}

func (s *scorer) Score(ctx context.Context, meaning string, lines []string) ([]float64, error) {
	scores, err := s.next.Score(ctx, meaning, lines)
	if err != nil {
		return nil, err
	}
	// Stored even when the run has been stopped: the answer was paid for, and
	// an answer is not less true for arriving after the user pressed Ctrl-C.
	// What must not happen is a file appearing after jevgrep has said it is
	// done -- nobody waits for the batches still in flight (§5.3) -- and that
	// is Close's job, not a context check here. A check would be a race:
	// whatever it decided, the store would still land afterwards.
	s.cache.Store(meaning, lines, scores)
	return scores, nil
}

// key identifies one question: this model, asked this meaning, about this
// line as it is actually sent.
//
// What is not in it matters as much as what is. The threshold and the shape of
// the expression are missing because they are decided locally -- which is what
// lets a second run tighten -t without sending anything. The file name and the
// line number are missing because the same text is the same question wherever
// it was read, and because a path in the cache would be the user's directory
// tree written down. The API key is missing because the cache belongs to the
// user of the machine, not to a credential.
func (c *Cache) key(meaning, query string) [keySize]byte {
	h := sha256.New()
	var n [8]byte

	field := func(s string) {
		// Length-prefixed so that no two different questions can be spelled
		// the same way once concatenated.
		binary.LittleEndian.PutUint64(n[:], uint64(len(s)))
		_, _ = h.Write(n[:])
		_, _ = h.Write([]byte(s))
	}
	binary.LittleEndian.PutUint64(n[:], formatVersion)
	_, _ = h.Write(n[:])
	field(c.model)
	field(meaning)
	field(query)

	return [keySize]byte(h.Sum(nil))
}

// today is the day stamp written into a record: whole days since the Unix
// epoch, which is all the resolution a month-long TTL needs and one less thing
// than a timestamp to say about when the user was searching.
func (c *Cache) today() uint32 {
	return uint32(c.now().Unix() / int64(24*time.Hour/time.Second))
}

func shardOf(k [keySize]byte) int { return int(k[0]) % shards }

// shard returns the in-memory answers of the file k belongs to, reading it the
// first time it is asked for. The caller holds c.mu.
func (c *Cache) shard(k [keySize]byte) map[[keySize]byte]float64 {
	i := shardOf(k)
	if c.loaded[i] == nil {
		c.loaded[i] = c.read(i)
	}
	return c.loaded[i]
}

func (c *Cache) path(shard int) string {
	return filepath.Join(c.dir, fmt.Sprintf("scores-%02x", shard))
}

// read loads one shard. Anything it cannot make sense of is left out rather
// than reported: a half-written record at the end of a file that two processes
// were appending to is expected, not a fault, and the only cost of dropping it
// is asking the model again.
func (c *Cache) read(shard int) map[[keySize]byte]float64 {
	out := make(map[[keySize]byte]float64)

	b, err := os.ReadFile(c.path(shard))
	if err != nil {
		return out
	}
	today := c.today()
	for len(b) >= recordSize {
		rec := b[:recordSize]
		b = b[recordSize:]

		day := binary.LittleEndian.Uint32(rec[keySize+8:])
		if today < day || today-day > ttlDays {
			continue
		}
		score := math.Float64frombits(binary.LittleEndian.Uint64(rec[keySize : keySize+8]))
		if score < 0 || score > 1 {
			continue
		}
		// Later records win: a rewritten answer is appended, never patched in
		// place.
		out[[keySize]byte(rec[:keySize])] = score
	}
	return out
}

func record(k [keySize]byte, score float64, day uint32) []byte {
	b := make([]byte, recordSize)
	copy(b, k[:])
	binary.LittleEndian.PutUint64(b[keySize:], math.Float64bits(score))
	binary.LittleEndian.PutUint32(b[keySize+8:], day)
	return b
}

// append adds records to a shard, emptying it first if it has grown past its
// budget. The caller holds c.mu.
func (c *Cache) append(shard int, data []byte) {
	flag := os.O_WRONLY | os.O_CREATE | os.O_APPEND

	// An overfull shard is emptied by reopening it truncated, not by
	// truncating the handle that is about to append to it: Windows grants an
	// O_APPEND handle every write right except FILE_WRITE_DATA, and moving
	// the end of a file needs exactly that one, so f.Truncate fails there
	// with a permission error -- leaving a shard that can be neither emptied
	// nor appended to, which would stop remembering anything at all, for
	// good, the first time it filled up.
	if info, err := os.Stat(c.path(shard)); err == nil && info.Size()+int64(len(data)) > maxShardBytes {
		flag = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		// What is on disk is gone, so what is in memory has to go too, or the
		// next run would be told about answers no longer written down.
		c.loaded[shard] = nil
	}

	f, err := os.OpenFile(c.path(shard), flag, cacheFileMode)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	// Written in small pieces so that each one is a single indivisible append:
	// other jevgrep processes may be appending to this very file, and there is
	// no lock anywhere in this package.
	for len(data) > 0 {
		n := min(len(data), appendBatch*recordSize)
		if _, err := f.Write(data[:n]); err != nil {
			return
		}
		data = data[n:]
	}
}
