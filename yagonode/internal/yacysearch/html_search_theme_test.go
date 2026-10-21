package yacysearch

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

func TestHTMLResultsUseYagoSeekThemeAndPreserveSearchScope(t *testing.T) {
	result := searchcore.Result{
		Title:   "A result",
		URL:     "https://example.org/page",
		Snippet: "A useful summary.",
	}
	search := &fakeSearch{
		response: searchcore.Response{
			TotalResults: 1,
			Results:      []searchcore.Result{result},
		},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"https://node.test/yacysearch.html?query=sample&resource=global",
		nil,
	)

	htmlEndpoint{search: search, suggestions: newRecentQueries()}.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"--blue:#1769aa", `href="/account/login"`,
		`name="resource"`, `value="global" selected`, `>A result</a>`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("rendered results missing %q", expected)
		}
	}
}
