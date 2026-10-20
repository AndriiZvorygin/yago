package personalsearch

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHistoryExportAndClearAreOwnerBoundAndCSRFProtected(t *testing.T) {
	learning := openLearning(t, time.Now)
	owner := Principal{AccountID: "history-owner", Personalization: true, CSRFToken: "owner-csrf"}
	other := Principal{AccountID: "history-other", Personalization: true, CSRFToken: "other-csrf"}
	if err := learning.RecordSearch(t.Context(), owner, "owner-only query"); err != nil {
		t.Fatal(err)
	}
	if err := learning.RecordSearch(t.Context(), other, "other-only query"); err != nil {
		t.Fatal(err)
	}
	handler := accountPagesHandler{learning: learning}
	get := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, PathAccountHistory, nil)
	request = request.WithContext(WithPrincipal(request.Context(), owner))
	handler.history(get, request)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), "owner-only query") ||
		strings.Contains(get.Body.String(), "other-only query") {
		t.Fatalf("owner history export status/body = %d, %s", get.Code, get.Body.String())
	}
	if got := get.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("history cache policy = %q", got)
	}

	apiKey := owner
	apiKey.APIKey = true
	apiRequest := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		PathAccountHistory,
		nil,
	)
	apiRequest = apiRequest.WithContext(WithPrincipal(apiRequest.Context(), apiKey))
	apiResponse := httptest.NewRecorder()
	handler.history(apiResponse, apiRequest)
	if apiResponse.Code != http.StatusUnauthorized {
		t.Fatalf("API key accessed browser history export: %d", apiResponse.Code)
	}

	missingCSRF := httptest.NewRecorder()
	clearRequest := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		PathAccountHistoryClear,
		strings.NewReader(""),
	)
	clearRequest = clearRequest.WithContext(WithPrincipal(clearRequest.Context(), owner))
	handler.clearHistory(missingCSRF, clearRequest)
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("history clear without CSRF status = %d", missingCSRF.Code)
	}

	form := url.Values{"csrf": {owner.CSRFToken}}
	clearRequest = httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		PathAccountHistoryClear,
		strings.NewReader(form.Encode()),
	)
	clearRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	clearRequest = clearRequest.WithContext(WithPrincipal(clearRequest.Context(), owner))
	cleared := httptest.NewRecorder()
	handler.clearHistory(cleared, clearRequest)
	if cleared.Code != http.StatusSeeOther {
		t.Fatalf("owner history clear status = %d, body=%s", cleared.Code, cleared.Body.String())
	}
	ownerRows, err := learning.History(t.Context(), owner.AccountID, 10)
	if err != nil || len(ownerRows) != 0 {
		t.Fatalf("owner history after clear = %+v, %v", ownerRows, err)
	}
	otherRows, err := learning.History(t.Context(), other.AccountID, 10)
	if err != nil || len(otherRows) != 1 || otherRows[0].Query != "other-only query" {
		t.Fatalf("other account history changed: %+v, %v", otherRows, err)
	}
}
