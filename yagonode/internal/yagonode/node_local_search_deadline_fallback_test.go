package yagonode

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

type localDeadlineFallbackProbe struct {
	response searchcore.Response
	err      error
	wait     bool
	calls    int
	deadline time.Time
}

func (s *localDeadlineFallbackProbe) Search(
	ctx context.Context,
	req searchcore.Request,
) (searchcore.Response, error) {
	s.calls++
	s.deadline, _ = ctx.Deadline()
	if s.wait {
		<-ctx.Done()
		return s.response, fmt.Errorf("probe search: %w", ctx.Err())
	}
	if s.err != nil {
		return s.response, s.err
	}
	s.response.Request = req
	return s.response, nil
}

func TestLocalSearchDeadlineFallbackKeepsFullTextWhenItFinishes(t *testing.T) {
	primary := &localDeadlineFallbackProbe{
		response: searchcore.Response{Results: []searchcore.Result{{URL: "primary"}}},
	}
	fallback := &localDeadlineFallbackProbe{}
	response, err := (localSearchDeadlineFallback{
		primary: primary, fallback: fallback, fallbackReserve: 50 * time.Millisecond,
	}).Search(
		requestDeadlineContext(t, time.Second),
		plainLocalFallbackRequest(),
	)
	if err != nil || primary.calls != 1 || fallback.calls != 0 ||
		len(response.Results) != 1 || response.Results[0].URL != "primary" {
		t.Fatalf(
			"response=%#v primary=%d fallback=%d err=%v",
			response,
			primary.calls,
			fallback.calls,
			err,
		)
	}
}

func TestLocalSearchDeadlineFallbackReturnsMarkedPartialResults(t *testing.T) {
	primary := &localDeadlineFallbackProbe{wait: true}
	fallback := &localDeadlineFallbackProbe{
		response: searchcore.Response{
			TotalResults: 1,
			Results: []searchcore.Result{
				{Title: "Needle Observatory", URL: "https://observatory.example/"},
			},
		},
	}
	request := plainLocalFallbackRequest()
	response, err := (localSearchDeadlineFallback{
		primary: primary, fallback: fallback, fallbackReserve: 50 * time.Millisecond,
	}).Search(requestDeadlineContext(t, 150*time.Millisecond), request)
	if err != nil || primary.calls != 1 || fallback.calls != 1 ||
		!primary.deadline.Before(fallback.deadline) || response.Request.Query != request.Query ||
		len(response.Results) != 1 || response.Results[0].URL != "https://observatory.example/" ||
		len(response.PartialFailures) != 1 ||
		response.PartialFailures[0] != localSearchDeadlinePartialFailure() {
		t.Fatalf(
			"response=%#v primary=%d fallback=%d err=%v",
			response,
			primary.calls,
			fallback.calls,
			err,
		)
	}
}

func TestLocalSearchDeadlineFallbackUsesRandomizedFallbackAfterPartialPrimary(t *testing.T) {
	primary := &localDeadlineFallbackProbe{
		response: searchcore.Response{
			Results: []searchcore.Result{{URL: "https://fixed-order.example/"}},
		},
		wait: true,
	}
	fallback := &localDeadlineFallbackProbe{
		response: searchcore.Response{
			Results: []searchcore.Result{{URL: "https://randomized.example/"}},
			Coverage: &searchcore.SearchCoverage{
				CompletedShards: 2,
				TotalShards:     8,
				Complete:        false,
			},
		},
	}
	response, err := (localSearchDeadlineFallback{
		primary: primary, fallback: fallback, fallbackReserve: 50 * time.Millisecond,
	}).Search(
		requestDeadlineContext(t, 150*time.Millisecond),
		plainLocalFallbackRequest(),
	)
	if err != nil || fallback.calls != 1 || len(response.Results) != 1 ||
		response.Results[0].URL != "https://randomized.example/" ||
		response.Coverage == nil || response.Coverage.CompletedShards != 2 ||
		len(response.PartialFailures) != 1 {
		t.Fatalf("response=%#v fallback=%d err=%v", response, fallback.calls, err)
	}
}

func TestLocalSearchDeadlineFallbackKeepsCompleteShardCoverageComplete(t *testing.T) {
	primary := &localDeadlineFallbackProbe{wait: true}
	fallback := &localDeadlineFallbackProbe{
		response: searchcore.Response{
			Results: []searchcore.Result{{URL: "https://observatory.example/"}},
			Coverage: &searchcore.SearchCoverage{
				CompletedShards: 8, TotalShards: 8, Complete: true,
			},
		},
	}
	response, err := (localSearchDeadlineFallback{
		primary: primary, fallback: fallback, fallbackReserve: 50 * time.Millisecond,
	}).Search(
		requestDeadlineContext(t, 150*time.Millisecond),
		plainLocalFallbackRequest(),
	)
	if err != nil || response.Coverage == nil || !response.Coverage.Complete ||
		len(response.PartialFailures) != 0 {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestLocalSearchDeadlineFallbackRejectsUnsupportedRequests(t *testing.T) {
	requests := []searchcore.Request{
		{
			Query: "Harborlight skyline dawn tide map",
			Terms: []string{"Harborlight", "skyline", "dawn", "tide", "map"},
		},
		{Query: "Harborlight", Terms: []string{"Harborlight"}, Phrases: []string{"Harborlight"}},
		{Query: "Harborlight", Terms: []string{"Harborlight"}, ExcludedTerms: []string{"spam"}},
		{Query: "Harborlight", Terms: []string{"Harborlight"}, Offset: 10},
		{Query: "Harborlight", Terms: []string{"Harborlight"}, SiteHost: "example.test"},
		{Query: "Harborlight", Terms: []string{"Harborlight"}, Language: "fr"},
		{
			Query:         "Harborlight",
			Terms:         []string{"Harborlight"},
			ContentDomain: searchcore.ContentDomainImage,
		},
		{Query: "Harborlight!", Terms: []string{"Harborlight"}},
	}
	for _, request := range requests {
		primary := &localDeadlineFallbackProbe{err: context.DeadlineExceeded}
		fallback := &localDeadlineFallbackProbe{}
		response, err := (localSearchDeadlineFallback{
			primary: primary, fallback: fallback, fallbackReserve: 10 * time.Millisecond,
		}).Search(requestDeadlineContext(t, time.Second), request)
		if !errors.Is(err, context.DeadlineExceeded) || fallback.calls != 0 ||
			len(response.PartialFailures) != 0 {
			t.Fatalf(
				"request=%#v response=%#v fallback=%d err=%v",
				request,
				response,
				fallback.calls,
				err,
			)
		}
	}
}

func TestProgressiveFallbackAcceptsShortPlainQueries(t *testing.T) {
	requests := []searchcore.Request{
		{Query: "Harborlight", Terms: []string{"Harborlight"}},
		{Query: "Harborlight skyline", Terms: []string{"Harborlight", "skyline"}},
		{
			Query: "Harborlight skyline dawn tide",
			Terms: []string{"Harborlight", "skyline", "dawn", "tide"},
		},
	}
	for _, request := range requests {
		if !progressiveFallbackEligible(request) {
			t.Fatalf("plain short query was rejected: %#v", request)
		}
	}
	if progressiveFallbackEligible(searchcore.Request{
		Query: "Harborlight skyline", Terms: []string{"Harborlight"},
	}) {
		t.Fatal("query whose parsed terms do not match its text was accepted")
	}
}

func TestLocalSearchDeadlineFallbackMarksResultsWhenProgressiveSearchFails(t *testing.T) {
	primary := &localDeadlineFallbackProbe{wait: true}
	fallback := &localDeadlineFallbackProbe{err: errors.New("title query failed")}
	response, err := (localSearchDeadlineFallback{
		primary: primary, fallback: fallback, fallbackReserve: 20 * time.Millisecond,
	}).Search(requestDeadlineContext(t, 80*time.Millisecond), plainLocalFallbackRequest())
	if err != nil || primary.calls != 1 || fallback.calls != 1 ||
		len(response.Results) != 0 || len(response.PartialFailures) != 1 {
		t.Fatalf(
			"response=%#v primary=%d fallback=%d err=%v",
			response,
			primary.calls,
			fallback.calls,
			err,
		)
	}
}

func TestLocalSearchDeadlineFallbackDoesNotStartWithoutParentDeadline(t *testing.T) {
	primary := &localDeadlineFallbackProbe{err: context.DeadlineExceeded}
	fallback := &localDeadlineFallbackProbe{}
	response, err := (localSearchDeadlineFallback{
		primary: primary, fallback: fallback, fallbackReserve: 50 * time.Millisecond,
	}).Search(context.Background(), plainLocalFallbackRequest())
	if !errors.Is(err, context.DeadlineExceeded) || primary.calls != 1 || fallback.calls != 0 ||
		len(response.PartialFailures) != 0 {
		t.Fatalf(
			"response=%#v primary=%d fallback=%d err=%v",
			response,
			primary.calls,
			fallback.calls,
			err,
		)
	}
}

func requestDeadlineContext(t *testing.T, timeout time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	t.Cleanup(cancel)

	return ctx
}

func plainLocalFallbackRequest() searchcore.Request {
	return searchcore.Request{
		Query:         "Needle",
		Terms:         []string{"Needle"},
		Source:        searchcore.SourceLocal,
		Limit:         10,
		ContentDomain: searchcore.ContentDomainText,
	}
}
