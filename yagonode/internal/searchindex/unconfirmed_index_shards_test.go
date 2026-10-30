package searchindex

import (
	"fmt"
	"testing"

	"github.com/D4rk4/yago/yagonode/internal/documentstore"
)

func TestIndexConfirmationDoesNotCrossShards(t *testing.T) {
	first := batchDoc("https://example.org/first", "first")
	second := batchDoc("https://example.org/second", "second")
	index, err := NewBleveDiskIndex(t.Context(), t.TempDir(), newFakeDocumentDirectory(first), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = index.Close() }()
	for suffix := 0; diskShardNumber(index.shards, first.NormalizedURL) == diskShardNumber(index.shards, second.NormalizedURL); suffix++ {
		second.NormalizedURL = fmt.Sprintf("https://example.org/second/%d", suffix)
	}
	failing := observeDocumentShard(index, first.NormalizedURL)
	successful := observeDocumentShard(index, second.NormalizedURL)
	failing.visibleFailure = true
	if err := index.IndexBatch(t.Context(), []documentstore.Document{first, second}); err == nil {
		t.Fatal("partial batch failure accepted")
	}
	if err := index.Index(t.Context(), second); err != nil || successful.writes != 2 {
		t.Fatalf("confirm successful shard: %v writes=%d", err, successful.writes)
	}
	if err := index.Index(t.Context(), first); err == nil || failing.writes != 2 {
		t.Fatalf("other shard cleared failure: %v writes=%d", err, failing.writes)
	}
	failing.visibleFailure = false
	if err := index.IndexBatch(t.Context(), []documentstore.Document{first, second}); err != nil {
		t.Fatal(err)
	}
	if failing.writes != 3 || successful.writes != 2 {
		t.Fatalf("recovery writes = %d, %d", failing.writes, successful.writes)
	}
	if err := index.IndexBatch(t.Context(), []documentstore.Document{first, second}); err != nil {
		t.Fatal(err)
	}
	if failing.writes != 3 || successful.writes != 2 {
		t.Fatal("confirmed shards rewritten")
	}
}
