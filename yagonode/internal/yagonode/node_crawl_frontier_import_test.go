package yagonode

import (
	"context"
	"errors"
	"testing"

	"github.com/D4rk4/yago/yagocrawlcontract"
	"github.com/D4rk4/yago/yagonode/internal/adminui"
)

type frontierPresenceFixture struct{ existing map[string]bool }

func (f frontierPresenceFixture) DocumentExists(
	_ context.Context,
	url string,
) (bool, error) {
	return f.existing[url], nil
}

func (f frontierPresenceFixture) DocumentsExist(
	_ context.Context,
	urls []string,
) ([]bool, error) {
	found := make([]bool, len(urls))
	for index, rawURL := range urls {
		found[index] = f.existing[rawURL]
	}
	return found, nil
}

type frontierOrderQueueFixture struct {
	key   string
	order yagocrawlcontract.CrawlOrder
	seen  map[string]bool
	calls int
}

func (f *frontierOrderQueueFixture) PublishOnce(
	_ context.Context,
	key string,
	order yagocrawlcontract.CrawlOrder,
) (bool, error) {
	f.calls++
	if f.seen[key] {
		return true, nil
	}
	f.seen[key] = true
	f.key = key
	f.order = order
	return false, nil
}

type frontierProfileWriterFixture struct {
	profiles int
	err      error
}

func (f *frontierProfileWriterFixture) RecordProfile(
	context.Context,
	yagocrawlcontract.CrawlProfile,
	yagocrawlcontract.CrawlOrderPriority,
) error {
	f.profiles++
	return f.err
}

func TestCrawlOrderImportSkipsIndexedURLsAndRetriesIdempotently(t *testing.T) {
	existingURL := "https://example.test/already-indexed"
	newURL := "https://example.test/pending"
	batch := validFrontierImportBatch(t, existingURL, newURL)
	queue := &frontierOrderQueueFixture{seen: make(map[string]bool)}
	profiles := new(frontierProfileWriterFixture)
	service := &crawlOrderImportService{
		documents: frontierPresenceFixture{existing: map[string]bool{existingURL: true}},
		orders:    queue,
		profiles:  profiles,
	}

	result, err := service.ImportCrawlOrder(t.Context(), batch)
	if err != nil {
		t.Fatalf("import frontier: %v", err)
	}
	if result.EnqueuedRequests != 1 || result.AlreadyIndexed != 1 || result.DuplicateBatch {
		t.Fatalf("first import result = %+v", result)
	}
	if len(queue.order.Requests) != 1 || queue.order.Requests[0].URL != newURL {
		t.Fatalf("persisted requests = %+v", queue.order.Requests)
	}
	if queue.key != batch.IdempotencyKey() || profiles.profiles != 1 {
		t.Fatalf("queue key %q, profile registrations %d", queue.key, profiles.profiles)
	}

	result, err = service.ImportCrawlOrder(t.Context(), batch)
	if err != nil {
		t.Fatalf("retry frontier: %v", err)
	}
	if !result.DuplicateBatch || result.EnqueuedRequests != 0 || result.AlreadyIndexed != 1 {
		t.Fatalf("retry result = %+v", result)
	}
	if queue.calls != 2 {
		t.Fatalf("queue calls = %d, want idempotent retry through queue", queue.calls)
	}
}

func TestCrawlOrderImportLeavesAllIndexedURLsOutOfQueue(t *testing.T) {
	firstURL := "https://example.test/one"
	secondURL := "https://example.test/two"
	batch := validFrontierImportBatch(t, firstURL, secondURL)
	queue := &frontierOrderQueueFixture{seen: make(map[string]bool)}
	service := &crawlOrderImportService{
		documents: frontierPresenceFixture{existing: map[string]bool{firstURL: true, secondURL: true}},
		orders:    queue,
		profiles:  new(frontierProfileWriterFixture),
	}
	result, err := service.ImportCrawlOrder(t.Context(), batch)
	if err != nil {
		t.Fatalf("import indexed frontier: %v", err)
	}
	if result.AlreadyIndexed != 2 || result.EnqueuedRequests != 0 || queue.calls != 0 {
		t.Fatalf("result %+v, queue calls %d", result, queue.calls)
	}
}

func TestCrawlOrderImportDoesNotPublishWhenProfilePersistenceFails(t *testing.T) {
	batch := validFrontierImportBatch(t, "https://example.test/page")
	queue := &frontierOrderQueueFixture{seen: make(map[string]bool)}
	service := &crawlOrderImportService{
		documents: frontierPresenceFixture{existing: map[string]bool{}},
		orders:    queue,
		profiles:  &frontierProfileWriterFixture{err: errors.New("storage unavailable")},
	}
	if _, err := service.ImportCrawlOrder(t.Context(), batch); err == nil {
		t.Fatal("profile storage failure was ignored")
	}
	if queue.calls != 0 {
		t.Fatal("order was published before profile persistence succeeded")
	}
}

func validFrontierImportBatch(t *testing.T, urls ...string) yagocrawlcontract.CrawlOrderImportBatch {
	t.Helper()
	profile := yagocrawlcontract.CrawlProfile{
		Handle:          "fixture-profile",
		Name:            "sanitized fixture",
		URLMustMatch:    yagocrawlcontract.MatchAll,
		MaxPagesPerHost: yagocrawlcontract.UnlimitedPagesPerHost,
	}
	requests := make([]yagocrawlcontract.CrawlRequest, 0, len(urls))
	for _, rawURL := range urls {
		requests = append(requests, yagocrawlcontract.CrawlRequest{
			URL:           rawURL,
			ProfileHandle: profile.Handle,
		})
	}
	batch := yagocrawlcontract.CrawlOrderImportBatch{
		Version:  1,
		SourceID: "sanitized-source",
		Profile:  profile,
		Requests: requests,
	}
	var err error
	batch.BatchID, err = batch.ComputeBatchID()
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

var _ adminui.CrawlOrderImporter = (*crawlOrderImportService)(nil)
