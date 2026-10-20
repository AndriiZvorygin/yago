package yacysearch

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

func TestHTMLEndpointDisplaysImportedTitleArray(t *testing.T) {
	search := &fakeSearch{response: searchcore.Response{
		TotalResults: 2,
		Results: []searchcore.Result{
			{
				Title: `["Sample page title", "Alternate title"]`,
				URL:   "https://example.org/imported",
			},
			{
				Title: "Ordinary page title",
				URL:   "https://example.org/ordinary",
			},
		},
	}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://node.test/yacysearch.html?query=fixture&resource=local",
		nil,
	)

	htmlEndpoint{search: search, suggestions: newRecentQueries()}.ServeHTTP(recorder, request)

	body := recorder.Body.String()
	for _, expected := range []string{
		">Sample page title</a>",
		">Ordinary page title</a>",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("HTML result page missing %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "[&#34;Sample page title&#34;") {
		t.Fatalf("HTML result retained serialized title array: %s", body)
	}
}
