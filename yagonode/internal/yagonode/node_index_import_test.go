package yagonode

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagocrawlcontract"
	"github.com/D4rk4/yago/yagonode/internal/adminui"
	"github.com/D4rk4/yago/yagonode/internal/documentstore"
)

type importDocumentDirectory map[string]documentstore.Document

func (d importDocumentDirectory) Document(
	_ context.Context,
	url string,
) (documentstore.Document, bool, error) {
	document, found := d[url]
	return document, found, nil
}

func (importDocumentDirectory) Count(context.Context) (int, error) { return 0, nil }

type importSubmitterFixture struct {
	batches []yagocrawlcontract.IngestBatch
	docs    importDocumentDirectory
	err     error
}

func (f *importSubmitterFixture) SubmitImport(
	_ context.Context,
	batch yagocrawlcontract.IngestBatch,
	_ yagocrawlcontract.CrawlProfile,
) error {
	f.batches = append(f.batches, batch)
	if f.err != nil {
		return f.err
	}
	if f.docs != nil {
		f.docs[batch.Document.NormalizedURL] = documentstore.Document{
			NormalizedURL: batch.Document.NormalizedURL,
			ContentHash:   batch.Document.ContentHash,
		}
	}
	return nil
}

type importScheduleFixture struct {
	urls     []string
	profiles []string
	err      error
}

func (f *importScheduleFixture) RecordProfile(
	_ context.Context,
	profile yagocrawlcontract.CrawlProfile,
	_ yagocrawlcontract.CrawlOrderPriority,
) error {
	f.profiles = append(f.profiles, profile.Handle)
	return f.err
}

func (f *importScheduleFixture) ScheduleAtIfAbsent(
	_ context.Context,
	url, _ string,
	_ time.Time,
	_ time.Duration,
) (bool, error) {
	f.urls = append(f.urls, url)
	return true, f.err
}

func TestDocumentImportDeduplicatesReplayAndPreservesConflictingDestination(t *testing.T) {
	const (
		presentURL   = "https://existing.example/page"
		conflictURL  = "https://conflict.example/page"
		canonicalURL = "https://canonical.example/page"
		mixedURL     = "https://mixed.example/page"
		newURL       = "https://new.example/page"
	)
	directory := importDocumentDirectory{
		presentURL: {NormalizedURL: presentURL, ContentHash: "same"},
		conflictURL: {
			NormalizedURL: conflictURL,
			ContentHash:   "destination",
		},
		canonicalURL: {
			NormalizedURL: canonicalURL,
			ContentHash:   "canonical-destination",
		},
		mixedURL: {NormalizedURL: mixedURL, ContentHash: "matching"},
		"https://mixed-canonical.example/page": {
			NormalizedURL: "https://mixed-canonical.example/page",
			ContentHash:   "canonical-destination",
		},
	}
	submitter := &importSubmitterFixture{docs: directory}
	schedules := new(importScheduleFixture)
	service := &documentImportService{
		documents: directory,
		ingest:    submitter,
		frontier:  schedules,
		profile:   importTestProfile(),
	}
	canonicalConflict := importTestRecord(
		"canonical-conflict",
		"https://alias.example/page",
		"source",
		nil,
	)
	canonicalConflict.Document.CanonicalURL = canonicalURL
	mixedConflict := importTestRecord("mixed-conflict", mixedURL, "matching", nil)
	mixedConflict.Document.CanonicalURL = "https://mixed-canonical.example/page"
	results, err := service.ImportDocuments(t.Context(), []adminui.IndexImportRecord{
		importTestRecord("existing", presentURL, "same", nil),
		importTestRecord("conflict", conflictURL, "source", nil),
		importTestRecord("new", newURL, "new-hash", nil),
		canonicalConflict,
		mixedConflict,
	})
	if err != nil {
		t.Fatalf("ImportDocuments: %v", err)
	}
	if len(results) != 5 || results[0].Status != "already-present" ||
		results[1].Status != "conflict" || results[2].Status != "imported" ||
		results[3].Status != "imported" || results[4].Status != "already-present" {
		t.Fatalf("results = %#v", results)
	}
	if len(submitter.batches) != 2 || submitter.batches[0].SourceURL != newURL ||
		submitter.batches[1].SourceURL != "https://alias.example/page" {
		t.Fatalf("submitted batches = %#v", submitter.batches)
	}
	if directory[conflictURL].ContentHash != "destination" {
		t.Fatal("conflicting import changed the existing destination document")
	}
	if directory[canonicalURL].ContentHash != "canonical-destination" {
		t.Fatal("alias import changed the existing canonical destination document")
	}
	if len(schedules.urls) != 0 {
		t.Fatalf("unscheduled documents entered the recrawl frontier: %#v", schedules.urls)
	}
	if len(schedules.profiles) != 1 {
		t.Fatalf("migration profile registrations = %d, want exactly one", len(schedules.profiles))
	}
}

func TestDocumentImportSchedulesOnlyExplicitVisitAndReplaysIdempotently(t *testing.T) {
	const url = "https://scheduled.example/page"
	nextVisit := time.Now().UTC().Add(-time.Minute)
	documents := importDocumentDirectory{}
	submitter := &importSubmitterFixture{docs: documents}
	service := &documentImportService{
		documents: documents,
		ingest:    submitter,
		frontier:  new(importScheduleFixture),
		profile:   importTestProfile(),
	}
	record := importTestRecord("row-1", url, "hash-1", &nextVisit)
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := service.ImportDocuments(
			t.Context(),
			[]adminui.IndexImportRecord{record},
		); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	if got := len(service.frontier.(*importScheduleFixture).urls); got != 2 {
		t.Fatalf(
			"schedule attempts = %d, want idempotent scheduling attempted on both retries",
			got,
		)
	}
	if service.frontier.(*importScheduleFixture).urls[0] != url {
		t.Fatal("unexpected scheduled URL")
	}
	if len(submitter.batches) != 1 {
		t.Fatalf("replayed import submitted %d documents, want 1", len(submitter.batches))
	}
	if got := len(service.frontier.(*importScheduleFixture).profiles); got != 1 {
		t.Fatalf("profile registration attempts = %d, want one", got)
	}
}

func TestDocumentImportReturnsIngestFailureWithoutAdvancingCaller(t *testing.T) {
	submitter := &importSubmitterFixture{
		docs: importDocumentDirectory{},
		err:  errors.New("storage unavailable"),
	}
	service := &documentImportService{
		documents: importDocumentDirectory{},
		ingest:    submitter,
		frontier:  new(importScheduleFixture),
		profile:   importTestProfile(),
	}
	if _, err := service.ImportDocuments(t.Context(), []adminui.IndexImportRecord{
		importTestRecord("row-1", "https://failure.example/", "hash", nil),
	}); err == nil {
		t.Fatal("ImportDocuments succeeded after the ingest path failed")
	}
	if len(submitter.batches) != 1 {
		t.Fatalf("submitted batches = %d, want 1", len(submitter.batches))
	}
}

func importTestProfile() yagocrawlcontract.CrawlProfile {
	return yagocrawlcontract.NewCrawlProfile(yagocrawlcontract.CrawlProfile{Name: "migration-test"})
}

func importTestRecord(id, url, contentHash string, nextVisit *time.Time) adminui.IndexImportRecord {
	return adminui.IndexImportRecord{
		ID:          id,
		NextVisitAt: nextVisit,
		Document: yagocrawlcontract.DocumentIngest{
			CanonicalURL:  url,
			NormalizedURL: url,
			ContentHash:   contentHash,
			ExtractedText: "fixture text",
			FetchedAt:     time.Now().UTC(),
		},
	}
}
