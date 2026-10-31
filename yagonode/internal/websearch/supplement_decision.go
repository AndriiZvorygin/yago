package websearch

import (
	"context"
	"fmt"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

type SupplementDecisionDelay func(context.Context) time.Duration

func WithSupplementDecisionDelay(delay SupplementDecisionDelay) Option {
	return func(searcher *FallbackSearcher) { searcher.supplementDecisionDelay = delay }
}

func (s *FallbackSearcher) searchWithSupplementDecision(
	ctx context.Context,
	req searchcore.Request,
	window searchcore.Request,
) (searchcore.Response, error) {
	branchContext, cancel := context.WithCancel(ctx)
	defer cancel()
	primary, snapshot := s.startPrimarySearch(branchContext, window)
	decision := time.NewTimer(s.supplementDecisionDelay(ctx))
	defer decision.Stop()
	select {
	case outcome := <-primary:
		if outcome.failure != nil {
			panic(outcome.failure)
		}
		if outcome.err != nil {
			return outcome.response, fmt.Errorf("primary search: %w", outcome.err)
		}

		return s.supplementPrimary(ctx, req, window, outcome.response)
	case <-decision.C:
	case <-ctx.Done():
	}
	provider := s.startProviderSearch(branchContext, window, snapshot)
	response, err := s.completeParallelSearch(
		ctx,
		branchContext,
		window,
		primary,
		provider,
	)

	return supplementalPage(response, req), err
}
