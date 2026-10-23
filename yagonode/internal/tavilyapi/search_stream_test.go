package tavilyapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/D4rk4/yago/yagonode/internal/documentstore"
	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

type progressiveAPISearcher struct{}

func (progressiveAPISearcher) Search(
	ctx context.Context,
	request searchcore.Request,
) (searchcore.Response, error) {
	partial := searchcore.Response{
		Request: request,
		Results: []searchcore.Result{{
			Title: "Partial result", URL: "https://example.test/page", Snippet: "partial text",
		}},
		PartialFailures: []searchcore.PartialFailure{{
			Source: searchcore.PartialFailureSourceLocalSearch,
			Reason: "incomplete shard coverage",
		}},
		Coverage: &searchcore.SearchCoverage{
			CompletedShards: 2, SampledShards: 1, TotalShards: 8, Complete: false,
		},
	}
	searchcore.ReportSearchProgress(ctx, searchcore.SearchProgress{
		Response: partial, CompletedShards: 2, SampledShards: 1, TotalShards: 8,
	})

	return partial, nil
}

func TestSearchStreamRequiresSearchAuthorization(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, progressiveAPISearcher{}, &fakeDocuments{},
		SearchAccessPolicy{BearerToken: searchTestKey}, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, PathSearchStream,
		strings.NewReader(`{"query":"sample"}`))
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized stream status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSearchStreamReturnsProgressAndExplicitPartialFinal(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, progressiveAPISearcher{}, &fakeDocuments{},
		SearchAccessPolicy{BearerToken: searchTestKey}, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, PathSearchStream,
		strings.NewReader(`{"query":"sample","max_results":3}`))
	request.Header.Set("Authorization", "Bearer "+searchTestKey)
	request.Header.Set(requestIDHeader, "stream-request")
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK ||
		recorder.Header().Get("Content-Type") != "application/x-ndjson; charset=utf-8" {
		t.Fatalf(
			"status=%d headers=%v body=%s",
			recorder.Code,
			recorder.Header(),
			recorder.Body.String(),
		)
	}
	var events []searchStreamEvent
	scanner := bufio.NewScanner(recorder.Body)
	for scanner.Scan() {
		var event searchStreamEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Type != "started" ||
		events[1].Type != "progress" || events[2].Type != "final" {
		t.Fatalf("stream events=%+v", events)
	}
	if events[1].RequestID != "stream-request" || events[1].Complete ||
		events[1].Coverage == nil || events[1].Coverage.CompletedShards != 2 ||
		events[1].Coverage.SampledShards != 1 ||
		len(events[1].Results) != 1 {
		t.Fatalf("progress event=%+v", events[1])
	}
	if events[2].Complete || events[2].Response == nil ||
		len(events[2].Response.PartialFailures) == 0 ||
		len(events[2].Response.Results) != 1 {
		t.Fatalf("final event=%+v", events[2])
	}
}

var _ documentstore.DocumentDirectory = (*fakeDocuments)(nil)
