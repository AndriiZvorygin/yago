package searchindex

import (
	"context"
	"testing"

	"github.com/D4rk4/yago/yagonode/internal/documentstore"
)

func TestVisibleFailedIndexWriteRequiresConfirmedRetry(t *testing.T) {
	for name, write := range map[string]func(*BleveDiskIndex, context.Context, documentstore.Document) error{
		"single": (*BleveDiskIndex).Index,
		"batch": func(index *BleveDiskIndex, ctx context.Context, document documentstore.Document) error {
			return index.IndexBatch(ctx, []documentstore.Document{document})
		},
	} {
		t.Run(name, func(t *testing.T) { requireVisibleWriteRecovery(t, write) })
	}
}

func requireVisibleWriteRecovery(
	t *testing.T,
	write func(*BleveDiskIndex, context.Context, documentstore.Document) error,
) {
	t.Helper()
	document := batchDoc("https://example.org/unconfirmed", "original")
	index, err := NewBleveDiskIndex(
		t.Context(),
		t.TempDir(),
		newFakeDocumentDirectory(document),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = index.Close() }()
	shard := observeDocumentShard(index, document.NormalizedURL)
	shard.visibleFailure = true
	if err := write(index, t.Context(), document); err == nil {
		t.Fatal("visible failed write accepted")
	}
	if err := write(index, t.Context(), document); err == nil || shard.writes != 2 {
		t.Fatalf("unconfirmed retry skipped: error=%v writes=%d", err, shard.writes)
	}
	shard.visibleFailure = false
	if err := write(index, t.Context(), document); err != nil || shard.writes != 3 {
		t.Fatalf("confirmation retry: error=%v writes=%d", err, shard.writes)
	}
	if err := write(index, t.Context(), document); err != nil || shard.writes != 3 {
		t.Fatalf("confirmed document rewritten: error=%v writes=%d", err, shard.writes)
	}
}
