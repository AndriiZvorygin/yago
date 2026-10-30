package crawlresults_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagocrawlcontract"
	"github.com/D4rk4/yago/yagonode/internal/boltvault"
	"github.com/D4rk4/yago/yagonode/internal/crawlresults"
	"github.com/D4rk4/yago/yagonode/internal/documentstore"
	"github.com/D4rk4/yago/yagonode/internal/searchindex"
	"github.com/D4rk4/yago/yagonode/internal/vault"
)

var errSearchIndexInterrupted = errors.New("search index interrupted")

type interruptibleSearchIndex struct {
	searchindex.SearchIndex
	interrupted *bool
}

func (i interruptibleSearchIndex) Index(ctx context.Context, doc documentstore.Document) error {
	if *i.interrupted {
		return errSearchIndexInterrupted
	}
	if err := i.SearchIndex.Index(ctx, doc); err != nil {
		return fmt.Errorf("index document: %w", err)
	}

	return nil
}

type interruptibleBatchSearchIndex struct {
	interruptibleSearchIndex
	batches searchindex.BatchIndexer
}

func (i interruptibleBatchSearchIndex) IndexBatch(
	ctx context.Context,
	docs []documentstore.Document,
) error {
	if *i.interrupted {
		return errSearchIndexInterrupted
	}
	if err := i.batches.IndexBatch(ctx, docs); err != nil {
		return fmt.Errorf("index batch: %w", err)
	}

	return nil
}

type recrawlPath struct {
	name string
	urls []string
	bulk bool
}

var recrawlPaths = []recrawlPath{
	{name: "single delivery document write", urls: []string{"https://example.org/harbor"}},
	{name: "single delivery batch write", urls: []string{"https://example.org/harbor"}, bulk: true},
	{name: "microbatch document writes", urls: []string{
		"https://example.org/harbor", "https://example.org/pier",
	}},
	{name: "microbatch batch write", urls: []string{
		"https://example.org/harbor", "https://example.org/pier",
	}, bulk: true},
}

func (p recrawlPath) pages(
	title, body string,
	fetchedAt time.Time,
) []yagocrawlcontract.IngestBatch {
	pages := make([]yagocrawlcontract.IngestBatch, 0, len(p.urls))
	for _, url := range p.urls {
		pages = append(pages, yagocrawlcontract.IngestBatch{
			SourceURL:     url,
			ObservationID: fetchedAt.Format(time.RFC3339Nano),
			ObservedAt:    fetchedAt,
			Document: yagocrawlcontract.DocumentIngest{
				NormalizedURL: url,
				Title:         title,
				ExtractedText: body,
				FetchedAt:     fetchedAt,
				IndexedAt:     fetchedAt,
			},
		})
	}

	return pages
}

type recrawlNode struct {
	root        string
	storage     *vault.Vault
	directory   documentstore.DocumentDirectory
	receiver    documentstore.DocumentReceiver
	index       *searchindex.BleveDiskIndex
	history     *crawlresults.URLObservationHistory
	urls        *recordingURLReceiver
	postings    *recordingPostingReceiver
	recorder    *recordingFetchRecorder
	interrupted bool
}

func openRecrawlNode(t *testing.T, root string) *recrawlNode {
	t.Helper()
	storage, err := boltvault.Open(filepath.Join(root, "node.db"), 0)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	directory, receiver, err := documentstore.Open(storage)
	if err != nil {
		t.Fatalf("open documents: %v", err)
	}
	stored, _ := directory.(documentstore.StoredDocuments)
	index, err := searchindex.NewBleveDiskIndex(
		t.Context(), filepath.Join(root, "bleve"), directory, stored)
	if err != nil {
		t.Fatalf("open search index: %v", err)
	}
	history, err := crawlresults.OpenURLObservationHistory(storage)
	if err != nil {
		t.Fatalf("open observation history: %v", err)
	}
	node := &recrawlNode{
		root: root, storage: storage, directory: directory, receiver: receiver,
		index: index, history: history, urls: &recordingURLReceiver{},
		postings: &recordingPostingReceiver{}, recorder: &recordingFetchRecorder{},
	}
	t.Cleanup(node.close)

	return node
}

func (n *recrawlNode) close() {
	_ = n.index.Close()
	_ = n.storage.Close()
}

func (n *recrawlNode) absorb(
	t *testing.T,
	bulk bool,
	batches ...yagocrawlcontract.IngestBatch,
) []string {
	t.Helper()
	stream := &fakeStream{out: make(chan crawlresults.IngestDelivery, len(batches))}
	outcomes := make(chan string, len(batches))
	for _, batch := range batches {
		stream.out <- crawlresults.IngestDelivery{
			Batch: batch,
			Ack:   func(context.Context) error { outcomes <- "ack"; return nil },
			Nak:   func(context.Context) error { outcomes <- "nak"; return nil },
		}
	}
	interruptible := interruptibleSearchIndex{SearchIndex: n.index, interrupted: &n.interrupted}
	var index searchindex.SearchIndex = interruptible
	if bulk {
		index = interruptibleBatchSearchIndex{interruptible, n.index}
	}
	consumer := crawlresults.NewIngestConsumerWithIndex(
		stream, n.receiver, index, n.urls, n.postings)
	consumer.OrderObservations(n.history)
	consumer.RecordFetches(n.recorder)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { consumer.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	settled := make([]string, 0, len(batches))
	for range batches {
		select {
		case outcome := <-outcomes:
			settled = append(settled, outcome)
		case <-time.After(10 * time.Second):
			t.Fatal("delivery did not settle")
		}
	}

	return settled
}

func (n *recrawlNode) requireSettled(
	t *testing.T,
	want string,
	bulk bool,
	batches ...yagocrawlcontract.IngestBatch,
) {
	t.Helper()
	for _, outcome := range n.absorb(t, bulk, batches...) {
		if outcome != want {
			t.Fatalf("delivery settled %q, want %q", outcome, want)
		}
	}
}

func (n *recrawlNode) indexUpdatedAt(t *testing.T) time.Time {
	t.Helper()
	stats, err := n.index.Stats(t.Context())
	if err != nil {
		t.Fatalf("index stats: %v", err)
	}

	return stats.UpdatedAt
}

func (n *recrawlNode) requireStoredFetchTimes(t *testing.T, urls []string, fetchedAt time.Time) {
	t.Helper()
	for _, url := range urls {
		doc, found, err := n.directory.Document(t.Context(), url)
		if err != nil || !found {
			t.Fatalf("stored document %s: found=%v err=%v", url, found, err)
		}
		if !doc.FetchedAt.Equal(fetchedAt) || !doc.IndexedAt.Equal(fetchedAt) {
			t.Fatalf("stored %s fetch times = %v/%v, want %v",
				url, doc.FetchedAt, doc.IndexedAt, fetchedAt)
		}
	}
}

func (n *recrawlNode) requireSearchable(t *testing.T, term string, urls []string, want bool) {
	t.Helper()
	results, err := n.index.Search(
		t.Context(),
		searchindex.SearchRequest{Query: term, MaxResults: 10},
	)
	if err != nil {
		t.Fatalf("search %q: %v", term, err)
	}
	found := make(map[string]bool, len(results.Results))
	for _, result := range results.Results {
		found[result.URL] = true
	}
	for _, url := range urls {
		if found[url] != want {
			t.Fatalf("search %q finds %s = %v, want %v", term, url, found[url], want)
		}
	}
}

var recrawlFirstFetch = time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)

func TestUnchangedRecrawlAcksWithoutSearchIndexMutation(t *testing.T) {
	for _, path := range recrawlPaths {
		t.Run(path.name, func(t *testing.T) {
			node := openRecrawlNode(t, t.TempDir())
			recrawled := recrawlFirstFetch.Add(24 * time.Hour)
			node.requireSettled(t, "ack", path.bulk,
				path.pages("Harbor ledger", "alpha soundings", recrawlFirstFetch)...)
			indexed := node.indexUpdatedAt(t)
			metadata, postings := node.urls.calls, node.postings.calls
			node.requireSettled(t, "ack", path.bulk,
				path.pages("Harbor ledger", "alpha soundings", recrawled)...)
			if updated := node.indexUpdatedAt(t); !updated.Equal(indexed) {
				t.Fatalf("index updated at %v after unchanged recrawl, want %v", updated, indexed)
			}
			if node.urls.calls == metadata || node.postings.calls == postings {
				t.Fatal("unchanged recrawl skipped the metadata or posting tail")
			}
			if !node.recorder.fetched.Equal(recrawled) {
				t.Fatalf("recorded fetch at %v, want %v", node.recorder.fetched, recrawled)
			}
			node.requireStoredFetchTimes(t, path.urls, recrawlFirstFetch)
			node.requireSearchable(t, "alpha", path.urls, true)
			postings = node.postings.calls
			node.requireSettled(t, "ack", path.bulk,
				path.pages("Harbor ledger", "alpha soundings", recrawled)...)
			if node.postings.calls != postings {
				t.Fatal("completed recrawl observation was absorbed again")
			}
		})
	}
}

func TestChangedRecrawlReindexesDocument(t *testing.T) {
	changes := []struct{ title, body, term string }{
		{title: "Lighthouse ledger", body: "alpha soundings", term: "lighthouse"},
		{title: "Lighthouse ledger", body: "bravo soundings", term: "bravo"},
	}
	for _, path := range recrawlPaths {
		t.Run(path.name, func(t *testing.T) {
			node := openRecrawlNode(t, t.TempDir())
			fetched := recrawlFirstFetch
			node.requireSettled(t, "ack", path.bulk,
				path.pages("Harbor ledger", "alpha soundings", fetched)...)
			for _, change := range changes {
				before := node.indexUpdatedAt(t)
				fetched = fetched.Add(time.Hour)
				node.requireSettled(t, "ack", path.bulk,
					path.pages(change.title, change.body, fetched)...)
				if node.indexUpdatedAt(t).Equal(before) {
					t.Fatalf("recrawl changing %q left the search index unchanged", change.term)
				}
				node.requireSearchable(t, change.term, path.urls, true)
				node.requireStoredFetchTimes(t, path.urls, fetched)
			}
		})
	}
}

func TestRecrawlRetryIndexesDocumentMissingAfterInterruptedWrite(t *testing.T) {
	for _, path := range recrawlPaths {
		t.Run(path.name, func(t *testing.T) {
			node := openRecrawlNode(t, t.TempDir())
			pages := path.pages("Harbor ledger", "alpha soundings", recrawlFirstFetch)
			node.interrupted = true
			node.requireSettled(t, "nak", path.bulk, pages...)
			node.requireSearchable(t, "alpha", path.urls, false)
			node.interrupted = false
			node.requireSettled(t, "ack", path.bulk, pages...)
			node.requireSearchable(t, "alpha", path.urls, true)
		})
	}
}

func TestRecrawlRetryAfterReopenRepairsStaleSearchIndex(t *testing.T) {
	for _, path := range recrawlPaths {
		t.Run(path.name, func(t *testing.T) {
			root := t.TempDir()
			node := openRecrawlNode(t, root)
			node.requireSettled(t, "ack", path.bulk,
				path.pages("Harbor ledger", "alpha soundings", recrawlFirstFetch)...)
			retried := path.pages(
				"Harbor ledger",
				"bravo soundings",
				recrawlFirstFetch.Add(time.Hour),
			)
			node.interrupted = true
			node.requireSettled(t, "nak", path.bulk, retried...)
			node.requireSearchable(t, "bravo", path.urls, false)
			node.close()
			node = openRecrawlNode(t, root)
			node.requireSettled(t, "ack", path.bulk, retried...)
			node.requireSearchable(t, "bravo", path.urls, true)
			node.requireSearchable(t, "alpha", path.urls, false)
		})
	}
}
