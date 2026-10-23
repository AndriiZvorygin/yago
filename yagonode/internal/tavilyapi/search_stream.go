package tavilyapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

type searchStreamEvent struct {
	Type            string                      `json:"type"`
	RequestID       string                      `json:"request_id"`
	Query           string                      `json:"query,omitempty"`
	Stage           string                      `json:"stage,omitempty"`
	Complete        bool                        `json:"complete"`
	Coverage        *searchcore.SearchCoverage  `json:"coverage,omitempty"`
	PartialFailures []searchcore.PartialFailure `json:"partial_failures,omitempty"`
	Results         []SearchResult              `json:"results,omitempty"`
	Response        *SearchResponse             `json:"response,omitempty"`
	Error           string                      `json:"error,omitempty"`
}

type searchStreamRequest struct {
	callerContext context.Context
	request       SearchRequest
	start         time.Time
	requestID     string
}

func progressStreamEvent(requestID string, progress searchcore.SearchProgress) searchStreamEvent {
	results := make([]SearchResult, 0, len(progress.Response.Results))
	for _, result := range progress.Response.Results {
		results = append(results, SearchResult{
			Title:   searchcore.DisplayTitle(result.Title),
			URL:     result.URL,
			Content: searchcore.DisplayDescription(result.Title, result.Snippet),
			Score:   result.Score,
		})
	}

	return searchStreamEvent{
		Type:      "progress",
		RequestID: requestID,
		Query:     progress.Response.Request.Query,
		Stage:     "local",
		Complete:  progress.Complete,
		Coverage: &searchcore.SearchCoverage{
			CompletedShards: progress.CompletedShards,
			SampledShards:   progress.SampledShards,
			TotalShards:     progress.TotalShards,
			Complete:        progress.Complete,
		},
		PartialFailures: progress.Response.PartialFailures,
		Results:         results,
	}
}

func (e searchEndpoint) serveStream(
	w http.ResponseWriter,
	r *http.Request,
	stream searchStreamRequest,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	streamContext, cancel := context.WithCancel(r.Context())
	defer cancel()
	var writeMu sync.Mutex
	writeEvent := func(event searchStreamEvent) bool {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := json.NewEncoder(w).Encode(event); err != nil {
			return false
		}
		flusher.Flush()

		return true
	}
	if !writeEvent(searchStreamEvent{
		Type: "started", RequestID: stream.requestID, Query: stream.request.Query,
	}) {
		cancel()
		return
	}
	streamContext = searchcore.WithSearchProgressObserver(
		streamContext,
		func(progress searchcore.SearchProgress) {
			if !writeEvent(progressStreamEvent(stream.requestID, progress)) {
				cancel()
			}
		},
	)
	response, err := e.searchResponseForCaller(
		streamContext,
		stream.callerContext,
		stream.request,
		stream.start,
		stream.requestID,
	)
	if err != nil {
		writeEvent(
			searchStreamEvent{
				Type: "error", RequestID: stream.requestID, Error: "search incomplete",
			},
		)
		return
	}
	writeEvent(searchStreamEvent{
		Type: "final", RequestID: stream.requestID, Query: response.Query,
		Complete: response.Complete, PartialFailures: response.PartialFailures,
		Coverage: response.Coverage, Results: response.Results, Response: &response,
	})
}
