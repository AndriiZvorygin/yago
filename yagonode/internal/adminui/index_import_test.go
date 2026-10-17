package adminui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagocrawlcontract"
)

type indexImporterFixture struct {
	calls      int
	err        error
	receipt    []IndexImportResult
	receiptSet bool
}

func (f *indexImporterFixture) ImportDocuments(
	_ context.Context,
	records []IndexImportRecord,
) ([]IndexImportResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.receiptSet {
		return f.receipt, nil
	}
	results := make([]IndexImportResult, 0, len(records))
	for _, record := range records {
		results = append(results, IndexImportResult{ID: record.ID, Status: "imported"})
	}
	return results, nil
}

type failedImportBody struct{}

func (failedImportBody) Read([]byte) (int, error) { return 0, errors.New("fixture read failure") }
func (failedImportBody) Close() error             { return nil }

func TestIndexImportValidatesAndDryRunsWithoutMutation(t *testing.T) {
	importer := new(indexImporterFixture)
	console := New(Options{IndexImport: importer})
	record := IndexImportRecord{
		ID: "fixture-1",
		Document: yagocrawlcontract.DocumentIngest{
			NormalizedURL: "https://example.test/page",
			ExtractedText: "sanitized fixture content",
		},
	}
	requestBody, err := json.Marshal(
		IndexImportRequest{Version: 1, DryRun: true, Records: []IndexImportRecord{record}},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"/admin/index/import",
		strings.NewReader(string(requestBody)),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	console.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"validated"`) {
		t.Fatalf("dry-run response = %d %s", response.Code, response.Body.String())
	}
	if importer.calls != 0 {
		t.Fatal("dry-run mutated the importer")
	}
	requestBody, err = json.Marshal(
		IndexImportRequest{Version: 1, Records: []IndexImportRecord{record}},
	)
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"/admin/index/import",
		strings.NewReader(string(requestBody)),
	)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	console.ServeHTTP(response, request)
	if response.Code != http.StatusOK || importer.calls != 1 {
		t.Fatalf(
			"import response = %d %s, calls %d",
			response.Code,
			response.Body.String(),
			importer.calls,
		)
	}
}

func TestIndexImportRejectsMalformedOrIncompleteBatchBeforeMutation(t *testing.T) {
	importer := new(indexImporterFixture)
	console := New(Options{IndexImport: importer})
	for _, requestBody := range []string{
		`{"version":2,"records":[]}`,
		`{"version":1,"records":[{"id":"missing-url","document":{"extractedText":"body"}}]}`,
		`{"version":1,"records":[{"id":"bad-url","document":{"normalizedURL":"file:///etc/passwd"}}]}`,
		`{"version":1,"records":[{"id":"bad-canonical","document":{"normalizedURL":"https://example.test/","canonicalURL":"https://user:pass@example.test/"}}]}`,
	} {
		request := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodPost,
			"/admin/index/import",
			strings.NewReader(requestBody),
		)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		console.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("request %s status = %d, want 400", requestBody, response.Code)
		}
	}
	if importer.calls != 0 {
		t.Fatal("invalid import reached the importer")
	}
}

func TestIndexImportBoundsRequestBody(t *testing.T) {
	console := New(Options{IndexImport: new(indexImporterFixture)})
	body := strings.Repeat(" ", yagocrawlcontract.MaximumIngestBatchBytes+1)
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"/admin/index/import",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	console.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized import status = %d, want 413", response.Code)
	}
}

func TestIndexImportRequestDecodeRejectsMalformedInputBeforeMutation(t *testing.T) {
	console := New(Options{IndexImport: new(indexImporterFixture)})
	valid := `{"version":1,"records":[{"id":"fixture","document":{"normalizedURL":"https://example.test/"}}]}`
	tests := []struct {
		name        string
		contentType string
		body        string
		readFailure bool
		wantStatus  int
	}{
		{name: "missing content type", body: valid, wantStatus: http.StatusUnsupportedMediaType},
		{
			name:        "invalid media type",
			contentType: "application/json; =bad",
			body:        valid,
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "unsupported media type",
			contentType: "text/plain",
			body:        valid,
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "body read failure",
			contentType: "application/json",
			readFailure: true,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "malformed JSON",
			contentType: "application/json",
			body:        `{`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "unknown field",
			contentType: "application/json",
			body:        `{"version":1,"unknown":true,"records":[]}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "trailing value",
			contentType: "application/json",
			body:        valid + ` {}`,
			wantStatus:  http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(
				t.Context(), http.MethodPost, "/admin/index/import", strings.NewReader(test.body),
			)
			if test.readFailure {
				request.Body = failedImportBody{}
			}
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			response := httptest.NewRecorder()
			console.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d: %s",
					response.Code,
					test.wantStatus,
					response.Body.String(),
				)
			}
		})
	}
}

func TestIndexImportUnavailableAndReceiptFailuresAreReported(t *testing.T) {
	requestBody := `{"version":1,"records":[{"id":"fixture","document":{"normalizedURL":"https://example.test/"}}]}`
	request := func(t *testing.T, console *Console) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodPost,
			"/admin/index/import",
			strings.NewReader(requestBody),
		)
		r.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		console.ServeHTTP(response, r)
		return response
	}
	if got := request(t, New(Options{})).Code; got != http.StatusNotFound {
		t.Fatalf("unavailable endpoint status = %d, want 404", got)
	}
	tests := []struct {
		name     string
		importer *indexImporterFixture
	}{
		{
			name:     "import error",
			importer: &indexImporterFixture{err: errors.New("fixture ingest failure")},
		},
		{name: "incomplete receipt", importer: &indexImporterFixture{receiptSet: true}},
		{
			name: "wrong receipt ID",
			importer: &indexImporterFixture{
				receiptSet: true,
				receipt:    []IndexImportResult{{ID: "other", Status: "imported"}},
			},
		},
		{
			name: "unknown receipt status",
			importer: &indexImporterFixture{
				receiptSet: true,
				receipt:    []IndexImportResult{{ID: "fixture", Status: "queued"}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := request(t, New(Options{IndexImport: test.importer}))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503: %s", response.Code, response.Body.String())
			}
			if test.importer.calls != 1 {
				t.Fatalf("importer calls = %d, want 1", test.importer.calls)
			}
		})
	}
}

func TestIndexImportRejectsInvalidDocumentTextAndURLs(t *testing.T) {
	assertIndexImportRecordsRejected(t, []indexImportRecordMutation{
		{name: "blank ID", change: func(r *IndexImportRecord) { r.ID = " \t" }},
		{name: "long ID", change: func(r *IndexImportRecord) { r.ID = strings.Repeat("x", 513) }},
		{name: "large text", change: func(r *IndexImportRecord) {
			r.Document.ExtractedText = strings.Repeat(
				"x",
				yagocrawlcontract.MaximumDocumentTextBytes+1,
			)
		}},
		{name: "large title", change: func(r *IndexImportRecord) {
			r.Document.Title = strings.Repeat("x", yagocrawlcontract.MaximumDocumentTitleBytes+1)
		}},
		{name: "too many headings", change: func(r *IndexImportRecord) {
			r.Document.Headings = make([]string, yagocrawlcontract.MaximumDocumentHeadings+1)
		}},
		{name: "large heading", change: func(r *IndexImportRecord) {
			r.Document.Headings = []string{
				strings.Repeat("x", yagocrawlcontract.MaximumDocumentHeadingBytes+1),
			}
		}},
		{
			name:   "missing normalized URL",
			change: func(r *IndexImportRecord) { r.Document.NormalizedURL = "" },
		},
		{
			name:   "unsafe normalized URL",
			change: func(r *IndexImportRecord) { r.Document.NormalizedURL = "https://user:pass@example.test/" },
		},
		{name: "oversized URL", change: func(r *IndexImportRecord) {
			r.Document.NormalizedURL = "https://example.test/" + strings.Repeat(
				"x",
				yagocrawlcontract.MaximumCrawlURLBytes+1,
			)
		}},
		{
			name:   "invalid canonical URL",
			change: func(r *IndexImportRecord) { r.Document.CanonicalURL = "file:///private" },
		},
	})
	if err := validateIndexImportRecord(validIndexImportRecord()); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
}

func TestIndexImportRejectsLinkMetadataAndScheduleLimits(t *testing.T) {
	assertIndexImportRecordsRejected(t, []indexImportRecordMutation{
		{name: "too many outlinks", change: func(r *IndexImportRecord) {
			r.Document.Outlinks = make([]string, yagocrawlcontract.MaximumDocumentOutlinks+1)
		}},
		{name: "too many inlinks", change: func(r *IndexImportRecord) {
			r.Document.Inlinks = make(
				[]yagocrawlcontract.AnchorText,
				yagocrawlcontract.MaximumDocumentAnchors+1,
			)
		}},
		{name: "too many outbound anchors", change: func(r *IndexImportRecord) {
			r.Document.OutboundAnchors = make(
				[]yagocrawlcontract.OutboundAnchor,
				yagocrawlcontract.MaximumDocumentAnchors+1,
			)
		}},
		{name: "too many metadata fields", change: func(r *IndexImportRecord) {
			r.Document.Metadata = map[string]string{}
			for i := 0; i <= yagocrawlcontract.MaximumDocumentMetadata; i++ {
				r.Document.Metadata[strings.Repeat("k", i+1)] = "v"
			}
		}},
		{name: "large metadata", change: func(r *IndexImportRecord) {
			r.Document.Metadata = map[string]string{
				"key": strings.Repeat("x", yagocrawlcontract.MaximumDocumentMetadataBytes+1),
			}
		}},
		{name: "zero next visit", change: func(r *IndexImportRecord) {
			zero := time.Time{}
			r.NextVisitAt = &zero
		}},
	})
}

func TestIndexImportRequestRecordCountBounds(t *testing.T) {
	valid := validIndexImportRecord()
	requests := []struct {
		name    string
		request IndexImportRequest
	}{
		{
			name:    "unsupported version",
			request: IndexImportRequest{Version: 2, Records: []IndexImportRecord{valid}},
		},
		{name: "empty records", request: IndexImportRequest{Version: 1}},
		{
			name: "too many records",
			request: IndexImportRequest{
				Version: 1,
				Records: make([]IndexImportRecord, indexImportMaxRecords+1),
			},
		},
		{
			name:    "duplicate IDs",
			request: IndexImportRequest{Version: 1, Records: []IndexImportRecord{valid, valid}},
		},
	}
	for _, test := range requests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateIndexImportRequest(test.request); err == nil {
				t.Fatal("invalid request was accepted")
			}
		})
	}
	maximum := make([]IndexImportRecord, indexImportMaxRecords)
	for index := range maximum {
		maximum[index] = validIndexImportRecord()
		maximum[index].ID = strings.Repeat("x", index+1)
	}
	if err := validateIndexImportRequest(
		IndexImportRequest{Version: 1, Records: maximum},
	); err != nil {
		t.Fatalf("maximum valid record count rejected: %v", err)
	}
}

type indexImportRecordMutation struct {
	name   string
	change func(*IndexImportRecord)
}

func validIndexImportRecord() IndexImportRecord {
	return IndexImportRecord{ID: "fixture", Document: yagocrawlcontract.DocumentIngest{
		NormalizedURL: "https://example.test/",
	}}
}

func assertIndexImportRecordsRejected(t *testing.T, tests []indexImportRecordMutation) {
	t.Helper()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := validIndexImportRecord()
			test.change(&record)
			if err := validateIndexImportRecord(record); err == nil {
				t.Fatal("invalid record was accepted")
			}
		})
	}
}
