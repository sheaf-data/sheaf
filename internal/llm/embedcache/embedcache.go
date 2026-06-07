// Package embedcache provides a content-addressable disk cache for
// embedding vectors so that re-runs over unchanged ContractElements
// don't re-pay for LLM calls.
//
// Per D2 (sheaf-design.md §15), the cache key includes:
//   - content_hash:    SHA-256 of the text being embedded
//   - provider_name:   e.g. "ollama-embed:nomic-embed-text"
//
// Both go into the key so switching providers/models invalidates
// automatically. Vectors are stored as little-endian float32 arrays
// on disk under `<root>/embeddings/<hash[0:2]>/<hash>.bin`.

package embedcache

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Embedder is the minimal subset of llm.Embedder this package needs.
// Defined locally to avoid an import cycle (the llm package depends
// on embedcache to wire BuildCache).
type Embedder interface {
	Name() string
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Cache is the interface; FSCache is the v1 implementation.
type Cache interface {
	// Get retrieves a cached vector. Returns (nil, false) on miss.
	Get(text, providerName string) ([]float32, bool)
	// Put stores a vector for (text, providerName). Errors are logged
	// at the call site; cache writes never block correctness.
	Put(text, providerName string, vec []float32) error
}

// FSCache is a filesystem-backed cache rooted at a directory.
type FSCache struct {
	root string
}

// New constructs an FSCache. The root directory is created if missing.
func New(root string) (*FSCache, error) {
	if root == "" {
		return nil, errors.New("embedcache: empty root")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("embedcache: mkdir %s: %w", root, err)
	}
	return &FSCache{root: root}, nil
}

// keyForText composes the cache key from (text, providerName).
func keyForText(text, providerName string) string {
	h := sha256.New()
	h.Write([]byte(providerName))
	h.Write([]byte{0})
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))
}

func (c *FSCache) pathFor(key string) string {
	return filepath.Join(c.root, "embeddings", key[:2], key+".bin")
}

// Get returns the cached vector for (text, providerName), or
// (nil, false) on miss.
func (c *FSCache) Get(text, providerName string) ([]float32, bool) {
	key := keyForText(text, providerName)
	f, err := os.Open(c.pathFor(key))
	if err != nil {
		return nil, false
	}
	defer f.Close()
	vec, err := decode(f)
	if err != nil {
		return nil, false
	}
	return vec, true
}

// Put writes the vector to disk. Uses atomic rename so concurrent
// writers don't corrupt entries.
func (c *FSCache) Put(text, providerName string, vec []float32) error {
	key := keyForText(text, providerName)
	full := c.pathFor(key)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp := full + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := encode(f, vec); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, full)
}

// EmbedWithCache returns vectors for every input, hitting the cache
// when possible and embedding only the misses. Updates the cache for
// every newly-computed vector. Order of returned vectors matches
// the order of `texts`.
//
// Errors from the embedder propagate; cache errors are silently
// swallowed (the cache is a perf optimization, not correctness).
func EmbedWithCache(ctx context.Context, e Embedder, c Cache, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	var missTexts []string
	var missIdx []int
	for i, t := range texts {
		if vec, ok := c.Get(t, e.Name()); ok {
			out[i] = vec
			continue
		}
		missTexts = append(missTexts, t)
		missIdx = append(missIdx, i)
	}
	if len(missTexts) == 0 {
		return out, nil
	}
	vecs, err := e.Embed(ctx, missTexts)
	if err != nil {
		return nil, err
	}
	for i, vec := range vecs {
		out[missIdx[i]] = vec
		_ = c.Put(missTexts[i], e.Name(), vec)
	}
	return out, nil
}

// ===========================================================
// On-disk format: 4-byte big-endian dim, then dim float32s little-endian.
// ===========================================================

func encode(w io.Writer, vec []float32) error {
	dim := uint32(len(vec))
	if err := binary.Write(w, binary.BigEndian, dim); err != nil {
		return err
	}
	return binary.Write(w, binary.LittleEndian, vec)
}

func decode(r io.Reader) ([]float32, error) {
	var dim uint32
	if err := binary.Read(r, binary.BigEndian, &dim); err != nil {
		return nil, err
	}
	if dim > 1<<20 { // sanity cap: 1M floats
		return nil, fmt.Errorf("embedcache: dim too large: %d", dim)
	}
	vec := make([]float32, dim)
	if err := binary.Read(r, binary.LittleEndian, &vec); err != nil {
		return nil, err
	}
	return vec, nil
}
