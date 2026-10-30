package yagonode

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/documentstore"
	"github.com/D4rk4/yago/yagonode/internal/searchcore"
	"github.com/D4rk4/yago/yagonode/internal/searchindex"
)

type storedURLDirectory struct {
	fakeDocDirectory
	stored map[string]bool
}

func (d storedURLDirectory) Document(
	_ context.Context,
	url string,
) (documentstore.Document, bool, error) {
	return documentstore.Document{}, d.stored[url], nil
}

type countingDocReceiver struct {
	fakeDocReceiver
	calls int
}

func (r *countingDocReceiver) Receive(
	ctx context.Context,
	docs []documentstore.Document,
) (documentstore.Receipt, error) {
	r.calls++

	return r.fakeDocReceiver.Receive(ctx, docs)
}

type batchRecordingIndex struct {
	recordingIndex
	batches [][]documentstore.Document
}

func (r *batchRecordingIndex) IndexBatch(
	_ context.Context,
	docs []documentstore.Document,
) error {
	r.batches = append(r.batches, docs)

	return nil
}

func newBatchRecordingCache(
	stored map[string]bool,
) (remoteIndexCache, *countingDocReceiver, *batchRecordingIndex) {
	receiver := &countingDocReceiver{}
	index := &batchRecordingIndex{}

	return remoteIndexCache{
		directory: storedURLDirectory{stored: stored},
		receiver:  receiver,
		index:     index,
		now:       func() time.Time { return time.Unix(1, 0) },
	}, receiver, index
}

func cachedDocumentURLs(docs []documentstore.Document) []string {
	urls := make([]string, 0, len(docs))
	for _, doc := range docs {
		urls = append(urls, doc.NormalizedURL)
	}

	return urls
}

func TestRemoteIndexCacheWritesNewResultsAsOneStoreAndIndexBatch(t *testing.T) {
	cache, receiver, index := newBatchRecordingCache(map[string]bool{
		"https://peer.example.net/stored": true,
	})

	cache.store(t.Context(), []searchcore.Result{
		{URL: "https://peer.example.net/a"},
		{URL: "https://peer.example.net/stored"},
		{URL: " https://peer.example.net/a "},
		{URL: "   "},
		{URL: "https://peer.example.net/b"},
		{URL: "https://peer.example.net/c"},
	})

	want := []string{
		"https://peer.example.net/a",
		"https://peer.example.net/b",
		"https://peer.example.net/c",
	}
	if receiver.calls != 1 || len(index.batches) != 1 || len(index.indexed) != 0 {
		t.Fatalf(
			"store calls %d, index batches %d, single index writes %d; want one of each batch",
			receiver.calls,
			len(index.batches),
			len(index.indexed),
		)
	}
	if got := cachedDocumentURLs(receiver.received); !slices.Equal(got, want) {
		t.Fatalf("stored %v, want %v", got, want)
	}
	if got := cachedDocumentURLs(index.batches[0]); !slices.Equal(got, want) {
		t.Fatalf("indexed %v, want %v", got, want)
	}
}

func TestRemoteIndexCacheWritesNothingWhenEveryResultIsCached(t *testing.T) {
	cache, receiver, index := newBatchRecordingCache(map[string]bool{
		"https://peer.example.net/stored": true,
	})

	cache.store(t.Context(), []searchcore.Result{
		{URL: "https://peer.example.net/stored"},
		{URL: " https://peer.example.net/stored "},
	})

	if receiver.calls != 0 || len(index.batches) != 0 {
		t.Fatalf("store calls %d, index batches %d, want none", receiver.calls, len(index.batches))
	}
}

func TestRemoteIndexCacheSkipsIndexBatchWhenStoreIsBusy(t *testing.T) {
	cache, receiver, index := newBatchRecordingCache(nil)
	receiver.busy = true

	cache.store(t.Context(), []searchcore.Result{
		{URL: "https://peer.example.net/a"},
		{URL: "https://peer.example.net/b"},
	})

	if receiver.calls != 1 || len(index.batches) != 0 {
		t.Fatalf("store calls %d, index batches %d, want one store and no index", receiver.calls,
			len(index.batches))
	}
}

func TestRemoteIndexCacheFindsEveryResultThroughPerDocumentIndex(t *testing.T) {
	cache, index, _ := newRoundTripCache(t)

	cache.store(t.Context(), []searchcore.Result{
		{URL: "https://peer.example.net/first", Snippet: "harbor lighthouse"},
		{URL: "https://peer.example.net/second", Snippet: "harbor ferry"},
	})

	results, err := index.Search(
		t.Context(),
		searchindex.SearchRequest{Query: "harbor", MaxResults: 5},
	)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if results.Total != 2 {
		t.Fatalf("Total = %d, want both cached remote results", results.Total)
	}
}
