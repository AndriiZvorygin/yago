package yagonode

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagocrawlcontract"
	"github.com/D4rk4/yago/yagonode/internal/adminui"
	"github.com/D4rk4/yago/yagonode/internal/crawlbroker"
	"github.com/D4rk4/yago/yagonode/internal/documentstore"
	"github.com/D4rk4/yago/yagonode/internal/recrawlfrontier"
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

type failingImportDocumentDirectory struct{ err error }

func (d failingImportDocumentDirectory) Document(
	context.Context,
	string,
) (documentstore.Document, bool, error) {
	return documentstore.Document{}, false, d.err
}

func (failingImportDocumentDirectory) Count(context.Context) (int, error) { return 0, nil }

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
	urls          []string
	profiles      []string
	profileErr    error
	scheduleError error
}

func (f *importScheduleFixture) RecordProfile(
	_ context.Context,
	profile yagocrawlcontract.CrawlProfile,
	_ yagocrawlcontract.CrawlOrderPriority,
) error {
	f.profiles = append(f.profiles, profile.Handle)
	return f.profileErr
}

func (f *importScheduleFixture) ScheduleAtIfAbsent(
	_ context.Context,
	url, _ string,
	_ time.Time,
	_ time.Duration,
) (bool, error) {
	f.urls = append(f.urls, url)
	return true, f.scheduleError
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

func TestDocumentImportReportsProfileLookupAndRegistrationFailures(t *testing.T) {
	t.Run("profile registration", func(t *testing.T) {
		service := &documentImportService{
			documents: importDocumentDirectory{},
			ingest:    new(importSubmitterFixture),
			frontier:  &importScheduleFixture{profileErr: errors.New("profile store unavailable")},
			profile:   importTestProfile(),
		}
		if _, err := service.ImportDocuments(t.Context(), nil); err == nil {
			t.Fatal("profile registration failure was hidden")
		}
	})
	t.Run("existing document lookup", func(t *testing.T) {
		service := &documentImportService{
			documents: failingImportDocumentDirectory{
				err: errors.New("document store unavailable"),
			},
			ingest:   new(importSubmitterFixture),
			frontier: new(importScheduleFixture),
			profile:  importTestProfile(),
		}
		if _, err := service.ImportDocuments(t.Context(), []adminui.IndexImportRecord{
			importTestRecord("row", "https://lookup.example/", "hash", nil),
		}); err == nil {
			t.Fatal("destination lookup failure was hidden")
		}
	})
}

func TestDocumentImportHonorsCancellationBeforeEachRecord(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	service := &documentImportService{
		documents:       importDocumentDirectory{},
		ingest:          new(importSubmitterFixture),
		frontier:        new(importScheduleFixture),
		profile:         importTestProfile(),
		profileRecorded: true,
	}
	if _, err := service.ImportDocuments(ctx, []adminui.IndexImportRecord{
		importTestRecord("row", "https://cancel.example/", "hash", nil),
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ImportDocuments error = %v, want context canceled", err)
	}
}

func TestDocumentImportReportsRecrawlScheduleFailures(t *testing.T) {
	tests := []struct {
		name      string
		document  documentstore.Document
		content   string
		wantCalls int
	}{
		{
			name: "already present",
			document: documentstore.Document{
				NormalizedURL: "https://scheduled-existing.example/",
				ContentHash:   "same",
			},
			content: "same",
		},
		{name: "new import", content: "new", wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const url = "https://scheduled-existing.example/"
			documents := importDocumentDirectory{}
			if test.document.NormalizedURL != "" {
				documents[url] = test.document
			}
			submitter := &importSubmitterFixture{docs: documents}
			schedule := &importScheduleFixture{scheduleError: errors.New("frontier unavailable")}
			service := &documentImportService{
				documents: documents,
				ingest:    submitter,
				frontier:  schedule,
				profile:   importTestProfile(),
			}
			nextVisit := time.Now().UTC().Add(time.Hour)
			_, err := service.ImportDocuments(t.Context(), []adminui.IndexImportRecord{
				importTestRecord("row", url, test.content, &nextVisit),
			})
			if err == nil {
				t.Fatal("recrawl schedule failure was hidden")
			}
			if got := len(submitter.batches); got != test.wantCalls {
				t.Fatalf("submitted batches = %d, want %d", got, test.wantCalls)
			}
			if len(schedule.urls) != 1 {
				t.Fatalf("schedule attempts = %d, want one", len(schedule.urls))
			}
		})
	}
}

func TestCrawlRuntimeImportDocumentsDelegatesResultsAndErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "success"},
		{name: "failure", err: errors.New("fixture ingest failure")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			submitter := &importSubmitterFixture{err: test.err}
			service := &documentImportService{
				documents: importDocumentDirectory{},
				ingest:    submitter,
				frontier:  new(importScheduleFixture),
				profile:   importTestProfile(),
			}
			runtime := &crawlRuntime{indexImport: service}
			results, err := runtime.ImportDocuments(t.Context(), []adminui.IndexImportRecord{
				importTestRecord("runtime-row", "https://runtime.example/", "hash", nil),
			})
			if test.err != nil {
				if err == nil || len(results) != 0 {
					t.Fatalf("ImportDocuments = %+v, %v; want wrapped error", results, err)
				}
				return
			}
			if err != nil || len(results) != 1 || results[0].Status != "imported" {
				t.Fatalf("ImportDocuments = %+v, %v", results, err)
			}
		})
	}
}

func importTestProfile() yagocrawlcontract.CrawlProfile {
	return yagocrawlcontract.NewCrawlProfile(yagocrawlcontract.CrawlProfile{Name: "migration-test"})
}

func TestNewDocumentImportServiceConfiguresAnImportOnlyProfile(t *testing.T) {
	documents := importDocumentDirectory{}
	service := newDocumentImportService(documents, nil, nil)
	if service.documents == nil || service.profile.Name != "Document migration recrawl" ||
		service.profile.Scope != yagocrawlcontract.ScopeWide ||
		service.profile.URLMustMatch != yagocrawlcontract.MatchAll ||
		service.profile.MaxPagesPerHost != 1 || !service.profile.AllowQueryURLs {
		t.Fatalf("migration service profile = %+v", service.profile)
	}
	if ingest, ok := service.ingest.(*crawlbroker.IngestReceiver); !ok || ingest != nil {
		t.Fatal("constructor replaced the supplied ingest dependency")
	}
	if frontier, ok := service.frontier.(*recrawlfrontier.Frontier); !ok || frontier != nil {
		t.Fatal("constructor replaced the supplied frontier dependency")
	}
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
