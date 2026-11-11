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
// for a single embedding request.
type catalogVectorCache struct {
	mu      sync.Mutex
	entries map[string]*catalogVectorEntry
}

type catalogVectorEntry struct {
	mu      sync.Mutex
	vectors [][]float32
}

func catalogVectorKey(host, model string, catalog []skills.Skill) string {
	h := sha256.New()
	for _, skill := range catalog {
		h.Write([]byte(skillRoutingText(skill)))
		h.Write([]byte{0})
	}
	return strings.TrimSpace(host) + "\x00" + model + "\x00" + hex.EncodeToString(h.Sum(nil))
}

func (c *catalogVectorCache) get(ctx context.Context, key string, load func(context.Context) ([][]float32, error)) ([][]float32, error) {
	c.mu.Lock()
	entry, ok := c.entries[key]
	if !ok {
		if len(c.entries) >= maxCatalogVectorEntries {
			clear(c.entries)
		}
		entry = &catalogVectorEntry{}
		c.entries[key] = entry
	}
	c.mu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.vectors != nil {
		return entry.vectors, nil
	}
	vectors, err := load(ctx)
	if err != nil {
		return nil, err
	}
	entry.vectors = vectors
	return vectors, nil
}
