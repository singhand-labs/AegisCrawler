package store

import (
	"context"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
)

// SealLLMCacheContent encrypts one cache value with workspace/key-bound AAD.
// Callers persist only the returned envelope and integrity hash.
func (s *Store) SealLLMCacheContent(ctx context.Context, cacheKey, content string) ([]byte, string, error) {
	return s.sealRequirementArtifact(
		authz.WorkspaceID(ctx),
		"llm-cache",
		cacheKey,
		"content",
		content,
	)
}

// OpenLLMCacheContent authenticates and decrypts one workspace-scoped cache
// value.
func (s *Store) OpenLLMCacheContent(ctx context.Context, cacheKey string, artifact []byte, expectedHash string) (string, error) {
	var content string
	err := s.openRequirementArtifact(
		authz.WorkspaceID(ctx),
		"llm-cache",
		cacheKey,
		"content",
		artifact,
		expectedHash,
		&content,
	)
	return content, err
}
