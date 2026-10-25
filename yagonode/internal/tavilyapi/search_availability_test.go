package tavilyapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/documentstore"
	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

type searchAvailabilityEndpointCase struct {
	name            string
	response        searchcore.Response
	err             error
	expiredRequest  bool
	canceledRequest bool
	status          int
}

func TestSearchEndpointDistinguishesIncompleteEmptySearch(t *testing.T) {
	for _, test := range searchAvailabilityEndpointCases() {
		t.Run(test.name, func(t *testing.T) {
			assertSearchAvailabilityEndpointCase(t, test)
		})
	}
}

func searchAvailabilityEndpointCases() []searchAvailabilityEndpointCase {
	return []searchAvailabilityEndpointCase{
		{
			name:     "honest miss",
			response: searchcore.Response{},
			status:   http.StatusOK,
		},
		{
			name: "incomplete miss",
			response: searchcore.Response{
				Coverage: &searchcore.SearchCoverage{
					CompletedShards: 2, TotalShards: 8, Complete: false,
				},
				PartialFailures: []searchcore.PartialFailure{{
					Source: searchcore.PartialFailureSourceLocalSearch,
					Reason: "deadline",
				}},
			},
			status: http.StatusOK,
		},
		{
			name: "incomplete miss with source error",
			response: searchcore.Response{PartialFailures: []searchcore.PartialFailure{{
				Source: searchcore.PartialFailureSourceLocalSearch,
				Reason: "unavailable",
			}}},
			err:    errors.New("source unavailable"),
			status: http.StatusOK,
		},
		{
			name: "source deadline incomplete miss",
			response: searchcore.Response{PartialFailures: []searchcore.PartialFailure{{
				Source: searchcore.PartialFailureSourceLocalSearch,
				Reason: "deadline",
			}}},
			err:    context.DeadlineExceeded,
			status: http.StatusOK,
		},
		{
			name: "expired caller incomplete miss",
			response: searchcore.Response{PartialFailures: []searchcore.PartialFailure{{
				Source: searchcore.PartialFailureSourceLocalSearch,
				Reason: "deadline",
			}}},
			err:            context.DeadlineExceeded,
			expiredRequest: true,
			status:         http.StatusInternalServerError,
		},
		{
			name: "canceled caller incomplete miss",
			response: searchcore.Response{PartialFailures: []searchcore.PartialFailure{{
				Source: searchcore.PartialFailureSourceLocalSearch,
				Reason: "canceled",
			}}},
			err:             context.Canceled,
			canceledRequest: true,
			status:          http.StatusInternalServerError,
		},
		{
			name: "usable partial response",
			response: searchcore.Response{
				Results: []searchcore.Result{{
					Title:   "Result",
					URL:     "https://example.org/result",
					Snippet: "available",
				}},
				PartialFailures: []searchcore.PartialFailure{{
					Source: searchcore.PartialFailureSourceRemoteYaCy,
					Reason: "deadline",
				}},
			},
			status: http.StatusOK,
		},
	}
}

func assertSearchAvailabilityEndpointCase(
	t *testing.T,
	test searchAvailabilityEndpointCase,
) {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx := searchAvailabilityRequestContext(t, test)
	req := httptest.NewRequestWithContext(
		ctx,
		http.MethodPost,
		PathSearch,
		strings.NewReader(`{"query":"topic"}`),
	)
	req.Header.Set("Authorization", "Bearer "+searchTestKey)
	newTestSearchEndpoint(
		&fakeSearcher{response: test.response, err: test.err},
		&fakeDocuments{rows: map[string]documentstore.Document{}},
	).ServeHTTP(rec, req)
	assertSearchAvailabilityStatus(t, rec, test.status)
	assertSearchAvailabilityResponse(t, rec, test)
}

func searchAvailabilityRequestContext(
	t *testing.T,
	test searchAvailabilityEndpointCase,
) context.Context {
	t.Helper()
	ctx := t.Context()
	if test.expiredRequest {
		expired, cancel := context.WithDeadline(ctx, time.Unix(0, 0))
		defer cancel()
		ctx = expired
	}
	if test.canceledRequest {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		ctx = canceled
	}
	return ctx
}

func assertSearchAvailabilityStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, status, rec.Body.String())
	}
	if status == http.StatusConflict &&
		!strings.Contains(rec.Body.String(), `"search unavailable`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if status == http.StatusConflict &&
		rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
}

func assertSearchAvailabilityResponse(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	test searchAvailabilityEndpointCase,
) {
	t.Helper()
	if test.status == http.StatusOK && hasLocalSearchPartialFailure(test.response.PartialFailures) {
		assertPartialSearchAvailabilityResponse(t, rec, test.response)
		return
	}
	if test.status == http.StatusOK && len(test.response.Results) == 0 &&
		len(test.response.PartialFailures) == 0 {
		assertHonestEmptySearchAvailabilityResponse(t, rec)
	}
}

func assertPartialSearchAvailabilityResponse(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	response searchcore.Response,
) {
	t.Helper()
	actual := decodeSearchResponse(t, rec)
	if actual.Complete || len(actual.PartialFailures) == 0 {
		t.Fatalf("partial empty response = %+v", actual)
	}
	if response.Coverage != nil &&
		(actual.Coverage == nil || actual.Coverage.CompletedShards != 2 ||
			actual.Coverage.TotalShards != 8) {
		t.Fatalf("partial coverage was lost: %+v", actual.Coverage)
	}
}

func assertHonestEmptySearchAvailabilityResponse(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	response := decodeSearchResponse(t, rec)
	if !response.Complete || len(response.PartialFailures) != 0 {
		t.Fatalf("honest empty response = %+v", response)
	}
}
