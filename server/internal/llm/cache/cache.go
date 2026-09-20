package cache

import (
	"context"
	"errors"
	"time"
)

var ErrUnsafeCacheContent = errors.New("llm cache content requires redaction")

// Entry is a single cached LLM completion result.
type Entry struct {
	Content      string
	InputTokens  int
	OutputTokens int
	ResponseID   string
	FinishReason string
	Provider     string
	Model        string
}

// Cache abstracts a persistent key/value cache for LLM completions.
type Cache interface {
	// Get returns a cached entry. The bool indicates whether the key was found
	// and has not expired.
	Get(ctx context.Context, key string) (*Entry, bool, error)

	// Set stores an entry with the given TTL. Implementations must reject
	// content that changes under defensive redaction: returning altered content
	// on a later hit would violate completion semantics, while persisting the
	// original would retain sensitive provider output. A TTL <= 0 means the
	// entry should not be cached.
	Set(ctx context.Context, key string, entry *Entry, ttl time.Duration) error
}
