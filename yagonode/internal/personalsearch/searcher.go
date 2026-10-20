package personalsearch

import (
	"context"
	"fmt"

	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

type Searcher struct {
	inner   searchcore.Searcher
	profile *Learning
	enabled func() bool
}

type HistoryRecorder struct {
	inner   searchcore.Searcher
	profile *Learning
	enabled func() bool
}

type principalContextKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	ctx = context.WithValue(ctx, principalContextKey{}, principal)
	if principal.AccountID != "" {
		ctx = searchcore.WithSessionScope(ctx, principal.AccountID)
	}
	return ctx
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok && principal.AccountID != ""
}

func NewSearcher(
	inner searchcore.Searcher,
	learning *Learning,
	enabled func() bool,
) searchcore.Searcher {
	if inner == nil || learning == nil {
		return inner
	}
	return Searcher{inner: inner, profile: learning, enabled: enabled}
}

func (s Searcher) Search(
	ctx context.Context,
	request searchcore.Request,
) (searchcore.Response, error) {
	response, err := s.inner.Search(ctx, request)
	if err != nil {
		return response, fmt.Errorf("search before personal reranking: %w", err)
	}
	principal, authenticated := PrincipalFromContext(ctx)
	if !authenticated || !principal.Personalization || !PersonalizationEnabled(s.enabled) {
		return response, nil
	}
	profile := profileOrEmpty(s.profile, ctx, principal.AccountID)

	return rerankForAccount(response, profile), nil
}

func profileOrEmpty(learning *Learning, ctx context.Context, accountID string) InterestProfile {
	profile, err := learning.Interests(ctx, accountID)
	if err != nil {
		return InterestProfile{Terms: map[string]float64{}, Hosts: map[string]float64{}}
	}

	return profile
}

func NewHistoryRecorder(
	inner searchcore.Searcher,
	learning *Learning,
	enabled func() bool,
) searchcore.Searcher {
	if inner == nil || learning == nil {
		return inner
	}
	return HistoryRecorder{inner: inner, profile: learning, enabled: enabled}
}

func (s HistoryRecorder) Search(
	ctx context.Context,
	request searchcore.Request,
) (searchcore.Response, error) {
	response, err := s.inner.Search(ctx, request)
	if err != nil || request.Offset != 0 {
		return response, wrapPersonalError("search before recording history", err)
	}
	principal, authenticated := PrincipalFromContext(ctx)
	if authenticated && principal.Personalization && PersonalizationEnabled(s.enabled) {
		_ = s.profile.RecordSearch(ctx, principal, request.SubmittedText())
	}

	return response, nil
}

func PersonalizationEnabled(enabled func() bool) bool {
	return enabled != nil && enabled()
}
