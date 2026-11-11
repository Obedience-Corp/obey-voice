package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
)

const maxCatalogVectorEntries = 16

var sharedCatalogVectors = &catalogVectorCache{entries: map[string]*catalogVectorEntry{}}

// catalogVectorCache shares skill catalog embeddings between brains in the
// process. Failures are not cached, and concurrent callers for one key wait
// for a single embedding request. Vectors live for the process lifetime, keyed
// on host, model name and catalog hash, so a model re-pulled under the same
// name needs a restart.
type catalogVectorCache struct {
	mu      sync.Mutex
	entries map[string]*catalogVectorEntry
}

type catalogVectorEntry struct {
	vectors [][]float32
	// done is closed when the load that owns the entry finishes.
	done chan struct{}
}

func catalogVectorKey(host, model string, catalog []skills.Skill) string {
	h := sha256.New()
	for _, skill := range catalog {
		h.Write([]byte(skillRoutingText(skill)))
		h.Write([]byte{0})
	}
	return strings.TrimSpace(host) + "\x00" + model + "\x00" + hex.EncodeToString(h.Sum(nil))
}

func (c *catalogVectorCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.entries)
}

// evictOneLocked drops a populated entry. Entries still loading are kept so
// their waiters keep sharing the request.
func (c *catalogVectorCache) evictOneLocked() {
	for key, entry := range c.entries {
		if entry.vectors != nil {
			delete(c.entries, key)
			return
		}
	}
}

func (c *catalogVectorCache) get(ctx context.Context, key string, load func(context.Context) ([][]float32, error)) ([][]float32, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		entry, ok := c.entries[key]
		if ok && entry.vectors != nil {
			vectors := entry.vectors
			c.mu.Unlock()
			return vectors, nil
		}
		if ok {
			done := entry.done
			c.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if len(c.entries) >= maxCatalogVectorEntries {
			c.evictOneLocked()
		}
		entry = &catalogVectorEntry{done: make(chan struct{})}
		c.entries[key] = entry
		c.mu.Unlock()
		return c.lead(ctx, key, entry, load)
	}
}

// lead runs the load for an entry this caller created. On failure the entry is
// removed so a waiter, or a later caller, makes a fresh attempt with its own
// context.
func (c *catalogVectorCache) lead(ctx context.Context, key string, entry *catalogVectorEntry, load func(context.Context) ([][]float32, error)) (vectors [][]float32, err error) {
	defer func() {
		c.mu.Lock()
		if err == nil && vectors != nil {
			entry.vectors = vectors
		} else if c.entries[key] == entry {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		close(entry.done)
	}()
	return load(ctx)
}
