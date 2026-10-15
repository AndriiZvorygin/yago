package adminui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/D4rk4/yago/yagocrawlcontract"
)

type indexImporterFixture struct {
	calls int
}

func (f *indexImporterFixture) ImportDocuments(
	_ context.Context,
	records []IndexImportRecord,
) ([]IndexImportResult, error) {
	f.calls++
	results := make([]IndexImportResult, 0, len(records))
	for _, record := range records {
		results = append(results, IndexImportResult{ID: record.ID, Status: "imported"})
	}
	return results, nil
}

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
