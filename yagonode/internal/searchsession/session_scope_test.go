package searchsession

import (
	"context"
	"fmt"
	"testing"

	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

type scopedSessionSearcher struct {
	calls int
}

func (s *scopedSessionSearcher) Search(
	ctx context.Context,
	request searchcore.Request,
) (searchcore.Response, error) {
	s.calls++
	scope := searchcore.SessionScope(ctx)
	results := make([]searchcore.Result, 4)
	for index := range results {
		results[index] = searchcore.Result{Title: fmt.Sprintf("%s-%d", scope, index+1)}
	}

	return searchcore.Response{
		Request:      request,
		TotalResults: len(results),
		Results:      results,
	}, nil
}

func TestStableWindowSeparatesAccountScopesAndKeepsScopedPaging(t *testing.T) {
	inner := &scopedSessionSearcher{}
	stable := NewStableWindow(inner)
	request := searchcore.Request{Query: "shared query", Limit: 1}

	first, err := stable.Search(
		searchcore.WithSessionScope(t.Context(), "account-a"),
		request,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := stable.Search(
		searchcore.WithSessionScope(t.Context(), "account-b"),
		request,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.Results[0].Title != "account-a-1" || second.Results[0].Title != "account-b-1" {
		t.Fatalf("scoped page one results: %q, %q", first.Results[0].Title, second.Results[0].Title)
	}

	request.Offset = 1
	first, err = stable.Search(
		searchcore.WithSessionScope(t.Context(), "account-a"),
		request,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err = stable.Search(
		searchcore.WithSessionScope(t.Context(), "account-b"),
		request,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.Results[0].Title != "account-a-2" || second.Results[0].Title != "account-b-2" {
		t.Fatalf("scoped page two results: %q, %q", first.Results[0].Title, second.Results[0].Title)
	}
	if inner.calls != 2 {
		t.Fatalf("deep searches = %d, want one per account", inner.calls)
	}
}

func TestRecentWindowCannotCrossAccountScopes(t *testing.T) {
	stable := NewStableWindow(&scopedSessionSearcher{})
	request := searchcore.Request{Query: "same"}
	if _, err := stable.Search(
		searchcore.WithSessionScope(t.Context(), "owner"),
		request,
	); err != nil {
		t.Fatal(err)
	}
	recent := stable.(ScopedRecentWindow)
	if _, found := recent.RecentScoped(
		searchcore.WithSessionScope(t.Context(), "other"),
		request,
	); found {
		t.Fatal("recent session leaked to another account")
	}
	if _, found := recent.RecentScoped(
		searchcore.WithSessionScope(t.Context(), "owner"),
		request,
	); !found {
		t.Fatal("owner could not reuse its recent session")
	}
}
