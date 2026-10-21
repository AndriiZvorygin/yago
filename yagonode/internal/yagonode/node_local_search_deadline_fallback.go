package yagonode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

const (
	localSearchFallbackReserve = 500 * time.Millisecond
	localSearchFallbackReason  = "title-only fallback used after full-text search exceeded its budget"
)

type localSearchDeadlineFallback struct {
	primary         searchcore.Searcher
	fallback        searchcore.Searcher
	fallbackReserve time.Duration
}

func withLocalSearchDeadlineFallback(
	primary searchcore.Searcher,
	fallback searchcore.Searcher,
) searchcore.Searcher {
	return localSearchDeadlineFallback{
		primary:         primary,
		fallback:        fallback,
		fallbackReserve: localSearchFallbackReserve,
	}
}

func (s localSearchDeadlineFallback) Search(
	ctx context.Context,
	req searchcore.Request,
) (searchcore.Response, error) {
	if s.fallback == nil || !titleOnlyFallbackEligible(req) {
		return s.searchPrimary(ctx, req)
	}
	deadline, found := ctx.Deadline()
	if !found {
		return s.searchPrimary(ctx, req)
	}
	primaryDeadline := deadline.Add(-s.fallbackReserve)
	if time.Now().Before(primaryDeadline) {
		primaryContext, cancel := context.WithDeadline(ctx, primaryDeadline)
		response, err := s.searchPrimary(primaryContext, req)
		cancel()
		if err == nil {
			return response, nil
		}
		if len(response.Results) > 0 {
			response.PartialFailures = append(
				response.PartialFailures,
				localSearchDeadlinePartialFailure(),
			)

			return response, nil
		}
		if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return response, err
		}
		return s.runFallback(ctx, req, response, err)
	}
	if ctx.Err() != nil {
		return searchcore.Response{}, fmt.Errorf(
			"local search request: %w",
			context.Cause(ctx),
		)
	}
	return s.runFallback(ctx, req, searchcore.Response{}, context.DeadlineExceeded)
}

func (s localSearchDeadlineFallback) searchPrimary(
	ctx context.Context,
	req searchcore.Request,
) (searchcore.Response, error) {
	response, err := s.primary.Search(ctx, req)
	if err != nil {
		return response, fmt.Errorf("full-text local search: %w", err)
	}

	return response, nil
}

func (s localSearchDeadlineFallback) runFallback(
	ctx context.Context,
	req searchcore.Request,
	primaryResponse searchcore.Response,
	primaryErr error,
) (searchcore.Response, error) {
	response, err := s.fallback.Search(ctx, req)
	if err != nil {
		return primaryResponse, fmt.Errorf("local search incomplete: %w", primaryErr)
	}
	response.Request = req
	response.PartialFailures = append(
		response.PartialFailures,
		localSearchDeadlinePartialFailure(),
	)

	return response, nil
}

func localSearchDeadlinePartialFailure() searchcore.PartialFailure {
	return searchcore.PartialFailure{
		Source: searchcore.PartialFailureSourceLocalSearch,
		Reason: localSearchFallbackReason,
		Diagnostic: searchcore.PartialFailureDiagnostic{
			Stage: searchcore.FailureStageLocalSearch,
			Cause: searchcore.FailureCauseDeadline,
		},
	}
}

func titleOnlyFallbackEligible(req searchcore.Request) bool {
	if len(req.Terms) == 0 || len(req.Terms) > 4 || req.Offset != 0 ||
		!plainFallbackQuery(req.Query, len(req.Terms)) ||
		(req.SubmittedQuery != "" && !plainFallbackQuery(req.SubmittedQuery, len(req.Terms))) ||
		len(req.ExcludedTerms) != 0 || len(req.Phrases) != 0 ||
		req.Fuzzy || req.WithFacets || req.Near || req.SortByDate ||
		req.Explain || req.RankingFeatures || req.MinDate != (time.Time{}) ||
		req.MaxDate != (time.Time{}) || req.MinFirstSeen != (time.Time{}) ||
		req.MaxFirstSeen != (time.Time{}) || len(req.IncludeDomains) != 0 ||
		len(req.ExcludeDomains) != 0 || req.SiteHost != "" || req.Language != "" ||
		req.InURL != "" || req.TLD != "" || req.FileType != "" || req.Author != "" ||
		req.URLMaskFilter != "" || req.PreferMaskFilter != "" ||
		len(req.ExpansionTerms) != 0 || req.Navigation != "" || req.Verify != "" {
		return false
	}
	switch req.ContentDomain {
	case "", searchcore.ContentDomainAll, searchcore.ContentDomainText:
		return true
	default:
		return false
	}
}

func plainFallbackQuery(query string, expectedTerms int) bool {
	words := strings.Fields(query)
	if expectedTerms <= 0 || len(words) != expectedTerms {
		return false
	}
	for _, word := range words {
		for _, character := range word {
			if !unicode.IsLetter(character) && !unicode.IsNumber(character) {
				return false
			}
		}
	}

	return true
}
