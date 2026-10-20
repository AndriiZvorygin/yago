package yacysearch

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

func TestHTMLEndpointRendersFirstNonemptyMultivalueTitle(t *testing.T) {
	search := &fakeSearch{response: searchcore.Response{
		TotalResults: 2,
		Results: []searchcore.Result{
			{
				Title:   `["", "Sample page title", "Alternate title"]`,
				Snippet: `["Sample page title"]`,
				URL:     "https://example.org/imported",
			},
			{Title: "Ordinary page title", URL: "https://example.org/ordinary"},
		},
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://node.test/yacysearch.html?query=fixture&resource=local",
		nil,
	)

	htmlEndpoint{search: search, suggestions: newRecentQueries()}.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, expected := range []string{
		">Sample page title</a>",
		">Sample page title</p>",
		">Ordinary page title</a>",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %q in %s", expected, body)
		}
	}
	if strings.Contains(body, `Alternate title</a>`) ||
		strings.Contains(
			body,
			`[&#34;&#34;, &#34;Sample page title&#34;, &#34;Alternate title&#34;]`,
		) {
		t.Fatalf("multivalue title was not normalized: %s", body)
	}
}
