package searchindex

import (
	"errors"
	"testing"
	"time"

	"github.com/blevesearch/bleve/v2"
	bleveindex "github.com/blevesearch/bleve_index_api"

	"github.com/D4rk4/yago/yagonode/internal/documentstore"
)

type observedDocumentShard struct {
	bleveIndexContract
	writes         int
	readFailure    bool
	writeFailure   bool
	visibleFailure bool
}

func (shard *observedDocumentShard) Document(id string) (bleveindex.Document, error) {
	if shard.readFailure {
		return nil, errors.New("document lookup failed")
	}
	document, err := shard.bleveIndexContract.Document(id)
	if err != nil {
		return nil, errors.New("document lookup failed")
	}
	return document, nil
}

func (shard *observedDocumentShard) Batch(batch *bleve.Batch) error {
	shard.writes++
	if shard.writeFailure {
		return errors.New("batch failed")
	}
	if err := shard.bleveIndexContract.Batch(batch); err != nil {
		return errors.New("batch failed")
	}
	if shard.visibleFailure {
		return errors.New("visible batch persistence failed")
	}
	return nil
}

func (shard *observedDocumentShard) Index(id string, data any) error {
	shard.writes++
	if shard.writeFailure {
		return errors.New("index failed")
	}
	if err := shard.bleveIndexContract.Index(id, data); err != nil {
		return errors.New("index failed")
	}
	if shard.visibleFailure {
		return errors.New("visible document persistence failed")
	}
	return nil
}

func observeDocumentShard(index *BleveDiskIndex, id string) *observedDocumentShard {
	position := diskShardNumber(index.shards, id)
	shard := &observedDocumentShard{bleveIndexContract: index.shards[position]}
	index.shards[position] = shard
	return shard
}

func TestUnchangedIndexedDocumentSurvivesReopen(t *testing.T) {
	document := batchDoc("https://example.org/stable", "original")
	directory := newFakeDocumentDirectory(document)
	path := t.TempDir()
	index, err := NewBleveDiskIndex(t.Context(), path, directory, nil)
	if err != nil {
		t.Fatal(err)
	}
	shard := observeDocumentShard(index, document.NormalizedURL)
	if err := index.Index(t.Context(), document); err != nil || shard.writes != 1 {
		t.Fatalf("initial write: %v, writes=%d", err, shard.writes)
	}
	updated := index.lastUpdate()
	document.FetchedAt = time.Now().UTC()
	document.IndexedAt = document.FetchedAt
	if err := index.Index(t.Context(), document); err != nil {
		t.Fatal(err)
	}
	if err := index.IndexBatch(t.Context(), []documentstore.Document{document}); err != nil {
		t.Fatal(err)
	}
	if shard.writes != 1 || index.lastUpdate() != updated {
		t.Fatalf("unchanged document wrote: writes=%d updated=%v", shard.writes, index.lastUpdate())
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	index, err = NewBleveDiskIndex(t.Context(), path, directory, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = index.Close() }()
	shard = observeDocumentShard(index, document.NormalizedURL)
	if err := index.IndexBatch(
		t.Context(),
		[]documentstore.Document{document},
	); err != nil ||
		shard.writes != 0 {
		t.Fatalf("reopened unchanged: %v, writes=%d", err, shard.writes)
	}
	document.Title = "replacement"
	if err := index.IndexBatch(
		t.Context(),
		[]documentstore.Document{document},
	); err != nil ||
		shard.writes != 1 {
		t.Fatalf("changed document: %v, writes=%d", err, shard.writes)
	}
}

func TestIndexedDocumentBatchPreservesLastRevision(t *testing.T) {
	original := batchDoc("https://example.org/order", "original")
	replacement := original
	replacement.Title = "replacement"
	index, err := NewBleveDiskIndex(
		t.Context(),
		t.TempDir(),
		newFakeDocumentDirectory(original),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = index.Close() }()
	shard := observeDocumentShard(index, original.NormalizedURL)
	if err := index.Index(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	for _, revisions := range [][]documentstore.Document{
		{replacement, original}, {original, replacement}, {replacement, replacement},
	} {
		if err := index.IndexBatch(t.Context(), revisions); err != nil {
			t.Fatal(err)
		}
		last, err := bleveDocumentFromStore(revisions[len(revisions)-1])
		if err != nil ||
			!shardHoldsIndexedForm(
				index.shards[diskShardNumber(index.shards, original.NormalizedURL)],
				original.NormalizedURL,
				last,
			) {
			t.Fatalf("last revision missing: %v", err)
		}
	}
	if shard.writes != 3 {
		t.Fatalf("writes=%d, want initial plus two changed batches", shard.writes)
	}
}

func TestIndexedDocumentMissingLegacyAndFailureRecovery(t *testing.T) {
	document := batchDoc("https://example.org/recovery", "original")
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
	legacy, err := bleveDocumentFromStore(document)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Candidate, err = encodeStoredCandidateProjection(newStoredCandidateProjection(document))
	if err != nil {
		t.Fatal(err)
	}
	if err := shard.bleveIndexContract.Index(document.NormalizedURL, legacy); err != nil {
		t.Fatal(err)
	}
	if err := index.Index(t.Context(), document); err != nil || shard.writes != 1 {
		t.Fatalf("legacy repair: %v, writes=%d", err, shard.writes)
	}
	shard.readFailure = true
	if err := index.Index(t.Context(), document); err != nil || shard.writes != 2 {
		t.Fatalf("uncertain identity skipped: %v, writes=%d", err, shard.writes)
	}
	shard.readFailure = false
	document.Title = "replacement"
	shard.writeFailure = true
	if err := index.IndexBatch(t.Context(), []documentstore.Document{document}); err == nil {
		t.Fatal("failed batch accepted")
	}
	shard.writeFailure = false
	if err := index.IndexBatch(
		t.Context(),
		[]documentstore.Document{document},
	); err != nil ||
		shard.writes != 4 {
		t.Fatalf("failed batch retry: %v, writes=%d", err, shard.writes)
	}
	if err := index.Delete(t.Context(), document.NormalizedURL); err != nil {
		t.Fatal(err)
	}
	if err := index.Index(t.Context(), document); err != nil || shard.writes != 5 {
		t.Fatalf("missing document repair: %v, writes=%d", err, shard.writes)
	}
}
