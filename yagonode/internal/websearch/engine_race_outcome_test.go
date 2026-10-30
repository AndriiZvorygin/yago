package websearch

import (
	"net/http"
	"testing"
)

func TestEngineRaceReportsHonestEmptyWhenAnEngineAnsweredWithoutRelevantRows(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Host == "failing.example" {
				return htmlResponse(http.StatusServiceUnavailable, ""), nil
			}

			return htmlResponse(http.StatusOK, "answer"), nil
		}),
	}
	provider := NewDDGSProvider(DDGSConfig{
		Client: client,
		Accept: VerifiedForQuery,
		Now:    fixedClock(),
	})
	provider.engines = []engine{
		tracingEngine("answering", Result{Title: "unrelated", URL: "https://unrelated.example"}),
		tracingEngine("failing", Result{}),
	}

	results, err := provider.Search(t.Context(), "needle", 10)
	if err != nil || len(results) != 0 {
		t.Fatalf("results=%+v error=%v, want honest empty answer", results, err)
	}
}
