package yagonode

import (
	"context"
	"strings"

	"github.com/D4rk4/yago/yagonode/internal/documentstore"
	"github.com/D4rk4/yago/yagonode/internal/searchcore"
	"github.com/D4rk4/yago/yagonode/internal/searchindex"
)

func (c remoteIndexCache) uncachedRemoteDocuments(
	ctx context.Context,
	results []searchcore.Result,
) []documentstore.Document {
	now := c.now()
	seen := make(map[string]struct{}, len(results))
	docs := make([]documentstore.Document, 0, len(results))
	for _, result := range results {
		url := strings.TrimSpace(result.URL)
		if url == "" {
			continue
		}
		if _, duplicate := seen[url]; duplicate {
			continue
		}
		seen[url] = struct{}{}
		if _, found, err := c.directory.Document(ctx, url); err != nil || found {
			continue
		}
		docs = append(docs, remoteResultDocument(result, now))
	}

	return docs
}

func (c remoteIndexCache) indexCachedDocuments(
	ctx context.Context,
	docs []documentstore.Document,
) {
	if bulk, ok := c.index.(searchindex.BatchIndexer); ok {
		_ = bulk.IndexBatch(ctx, docs)

		return
	}
	for _, doc := range docs {
		_ = c.index.Index(ctx, doc)
	}
}
