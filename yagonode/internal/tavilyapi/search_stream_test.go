package tavilyapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		Results: []searchcore.Result{
			{
				Title:   `["Partial result", "Alternate"]`,
				URL:     "https://example.test/page",
				Snippet: `["Partial result"]`,
			},
		},
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

type streamSearchProbe struct {
	err          error
	progressSeen bool
	ctxCanceled  bool
}

func (probe *streamSearchProbe) Search(
	ctx context.Context,
	request searchcore.Request,
) (searchcore.Response, error) {
	searchcore.ReportSearchProgress(ctx, searchcore.SearchProgress{
		Response: searchcore.Response{Request: request},
	})
	probe.progressSeen = true
	probe.ctxCanceled = ctx.Err() != nil
	return searchcore.Response{Request: request}, probe.err
}

type nonFlushingStreamWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (writer *nonFlushingStreamWriter) Header() http.Header {
	if writer.header == nil {
		writer.header = make(http.Header)
	}

	return writer.header
}

func (writer *nonFlushingStreamWriter) WriteHeader(status int) {
	writer.status = status
}

func (writer *nonFlushingStreamWriter) Write(data []byte) (int, error) {
	_, _ = writer.body.Write(data)
	return len(data), nil
}

type failingFlushingStreamWriter struct {
	nonFlushingStreamWriter
	writes int
	failAt int
}

func (writer *failingFlushingStreamWriter) Write(data []byte) (int, error) {
	writer.writes++
	if writer.writes == writer.failAt {
		return 0, errors.New("client disconnected")
	}

	_, _ = writer.body.Write(data)
	return len(data), nil
}

func (*failingFlushingStreamWriter) Flush() {}

func TestServeStreamHandlesUnavailableAndFailedWriters(t *testing.T) {
	stream := searchStreamRequest{
		callerContext: t.Context(),
		request:       SearchRequest{Query: "sample"},
		start:         time.Now(),
		requestID:     "stream-test",
	}
	probe := &streamSearchProbe{}
	endpoint := searchEndpoint{search: probe, documents: &fakeDocuments{}, now: time.Now}

	nonFlushing := &nonFlushingStreamWriter{}
	endpoint.serveStream(nonFlushing, httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, PathSearchStream, nil,
	), stream)
	if nonFlushing.status != http.StatusInternalServerError || probe.progressSeen {
		t.Fatalf("non-flushing status=%d search-called=%t", nonFlushing.status, probe.progressSeen)
	}

	startFailure := &failingFlushingStreamWriter{failAt: 1}
	endpoint.serveStream(startFailure, httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, PathSearchStream, nil,
	), stream)
	if probe.progressSeen {
		t.Fatal("search ran after the started event could not be written")
	}

	probe = &streamSearchProbe{}
	endpoint.search = probe
	progressFailure := &failingFlushingStreamWriter{failAt: 2}
	endpoint.serveStream(progressFailure, httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, PathSearchStream, nil,
	), stream)
	if !probe.progressSeen || !probe.ctxCanceled {
		t.Fatalf("progress write did not cancel stream context: %+v", probe)
	}

	probe = &streamSearchProbe{err: errors.New("search backend failed")}
	endpoint.search = probe
	errorWriter := httptest.NewRecorder()
	endpoint.serveStream(errorWriter, httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, PathSearchStream, nil,
	), stream)
	if !strings.Contains(errorWriter.Body.String(), `"type":"error"`) {
		t.Fatalf("search error stream=%q", errorWriter.Body.String())
	}
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
		events[1].Coverage.SampledShards != 1 || len(events[1].Results) != 1 ||
		events[1].Results[0].Title != "Partial result" ||
		events[1].Results[0].Content != "Partial result" ||
		len(events[2].Results) != 1 || events[2].Results[0].Title != "Partial result" ||
		events[2].Results[0].Content != "Partial result" {
		t.Fatalf("progress event=%+v", events[1])
	}
	if events[2].Complete || events[2].Response == nil ||
		len(events[2].Response.PartialFailures) == 0 ||
		len(events[2].Response.Results) != 1 {
		t.Fatalf("final event=%+v", events[2])
	}
}

var _ documentstore.DocumentDirectory = (*fakeDocuments)(nil)
