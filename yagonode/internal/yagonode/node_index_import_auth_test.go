package yagonode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/D4rk4/yago/yagonode/internal/adminauth"
	"github.com/D4rk4/yago/yagonode/internal/adminui"
)

type indexImportAuthFixture struct {
	calls int
}

func (f *indexImportAuthFixture) ImportDocuments(
	_ context.Context,
	records []adminui.IndexImportRecord,
) ([]adminui.IndexImportResult, error) {
	f.calls++
	return []adminui.IndexImportResult{{ID: records[0].ID, Status: "imported"}}, nil
}

func TestIndexImportRequiresScopedAdminWriteKey(t *testing.T) {
	service, err := provisionAdminAuth(context.Background(), nodeConfig{}, openTestVault(t), nil)
	if err != nil {
		t.Fatalf("provision auth: %v", err)
	}
	readKey, err := service.CreateAPIKey(
		context.Background(),
		"read-only import test",
		[]string{string(adminauth.ScopeAdminRead)},
	)
	if err != nil {
		t.Fatalf("create read key: %v", err)
	}
	writeKey, err := service.CreateAPIKey(
		context.Background(),
		"write import test",
		[]string{string(adminauth.ScopeAdminWrite)},
	)
	if err != nil {
		t.Fatalf("create write key: %v", err)
	}
	importer := new(indexImportAuthFixture)
	ops := http.NewServeMux()
	ops.Handle(adminui.BasePath, adminui.New(adminui.Options{IndexImport: importer}))
	handler := guardAdminSurface(service, ops)
	body := `{"version":1,"records":[{"id":"fixture-row","document":{"normalizedURL":"https://fixture.example.test/page","contentHash":"fixture-hash","extractedText":"safe fixture"}}]}`
	request := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodPost,
			"/admin/index/import",
			strings.NewReader(body),
		)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	if got := request(""); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated import = %d, want 401", got.Code)
	}
	if got := request(readKey.Secret); got.Code != http.StatusForbidden {
		t.Fatalf("admin:read import = %d, want 403", got.Code)
	}
	if got := request(writeKey.Secret); got.Code != http.StatusOK {
		t.Fatalf("admin:write import = %d %s, want 200", got.Code, got.Body.String())
	}
	if importer.calls != 1 {
		t.Fatalf("importer calls = %d, want only the authorized write", importer.calls)
	}
}
