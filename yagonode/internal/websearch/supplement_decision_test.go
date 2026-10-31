package websearch

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

func fixedSupplementDecision(delay time.Duration) Option {
	return WithSupplementDecisionDelay(func(context.Context) time.Duration { return delay })
}

func TestSupplementDecisionKeepsSequentialSupplementForPromptPrimary(t *testing.T) {
	for _, size := range []int{1, SupplementalCandidateTarget} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			rows := make([]searchcore.Result, size)
			for ordinal := range rows {
				rows[ordinal] = searchcore.Result{
					URL:   fmt.Sprintf("https://primary.example/%d", ordinal),
					Title: "Needle reference",
				}
			}
			provider := &stubProvider{
				results: []Result{{URL: "https://web.example/new", Title: "Needle reference"}},
			}
			response, err := NewFallbackSearcher(
				&stubSearcher{resp: searchcore.Response{Results: rows}},
				provider,
				enabled,
				fixedSupplementDecision(time.Hour),
			).Search(t.Context(), searchcore.Request{Query: "needle", Limit: 1})
			if err != nil || len(response.Results) != 1 ||
				(provider.calls > 0) != (size < SupplementalCandidateTarget) {
				t.Fatalf("rows=%d calls=%d error=%v", len(response.Results), provider.calls, err)
			}
		})
	}
}

type webGatedPrimary struct{ webStarted <-chan struct{} }

func (p webGatedPrimary) Search(
	ctx context.Context,
	_ searchcore.Request,
) (searchcore.Response, error) {
	select {
	case <-p.webStarted:
	case <-ctx.Done():
		return searchcore.Response{}, fmt.Errorf("gated primary: %w", ctx.Err())
	}

	return searchcore.Response{Results: []searchcore.Result{{
		URL: "https://swarm.example/needle", Title: "Needle reference",
	}}}, nil
}

type signalingProvider struct {
	started chan struct{}
	results []Result
}

func (p signalingProvider) Search(context.Context, string, int) ([]Result, error) {
	close(p.started)

	return p.results, nil
}

func TestSupplementDecisionStartsWebBesideSlowPrimaryAndKeepsBoth(t *testing.T) {
	webStarted := make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	response, err := NewFallbackSearcher(
		webGatedPrimary{webStarted: webStarted},
		signalingProvider{
			started: webStarted,
			results: []Result{{URL: "https://web.example/needle", Title: "Needle reference"}},
		},
		enabled,
		fixedSupplementDecision(5*time.Millisecond),
	).Search(ctx, searchcore.Request{Query: "needle", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	urls := map[string]bool{}
	for _, result := range response.Results {
		urls[result.URL] = true
	}
	if !urls["https://swarm.example/needle"] || !urls["https://web.example/needle"] {
		t.Fatalf("results=%+v", response.Results)
	}
}

func TestSupplementDecisionReturnsPromptPrimaryFailure(t *testing.T) {
	provider := &stubProvider{}
	_, err := NewFallbackSearcher(
		&stubSearcher{err: fmt.Errorf("index unavailable")},
		provider,
		enabled,
		fixedSupplementDecision(time.Hour),
	).Search(t.Context(), searchcore.Request{Query: "needle", Limit: 10})
	if err == nil || provider.calls != 0 {
		t.Fatalf("calls=%d error=%v", provider.calls, err)
	}
}

func TestSupplementDecisionSkipsIneligibleRequests(t *testing.T) {
	primary := &stubSearcher{resp: searchcore.Response{Results: []searchcore.Result{{
		URL: "https://primary.example/only", Title: "Needle reference",
	}}}}
	provider := &stubProvider{}
	response, err := NewFallbackSearcher(
		primary,
		provider,
		disabled,
		fixedSupplementDecision(0),
	).Search(t.Context(), searchcore.Request{Query: "needle", Limit: 10})
	if err != nil || provider.calls != 0 || len(response.Results) != 1 {
		t.Fatalf("calls=%d results=%+v error=%v", provider.calls, response.Results, err)
	}
}

type panickingSupplementPrimary struct{}

func (panickingSupplementPrimary) Search(
	context.Context,
	searchcore.Request,
) (searchcore.Response, error) {
	panic("primary defect")
}

func TestSupplementDecisionPropagatesPromptPrimaryPanic(t *testing.T) {
	provider := &stubProvider{}
	defer func() {
		if recovered := recover(); recovered != "primary defect" || provider.calls != 0 {
			t.Fatalf("recovered=%v calls=%d", recovered, provider.calls)
		}
	}()
	_, _ = NewFallbackSearcher(
		panickingSupplementPrimary{},
		provider,
		enabled,
		fixedSupplementDecision(time.Hour),
	).Search(t.Context(), searchcore.Request{Query: "needle", Limit: 10})
	t.Fatal("primary panic was swallowed")
}
