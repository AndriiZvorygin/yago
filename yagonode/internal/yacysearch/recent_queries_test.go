package yacysearch

import (
	"context"
	"testing"

	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

func TestPublicSuggestionsExcludeAccountScopedQueries(t *testing.T) {
	recent := newRecentQueries()
	recent.RecordSearch(context.Background(), "public query")
	recent.RecordSearch(
		searchcore.WithSessionScope(context.Background(), "account-a"),
		"private account query",
	)
	got := recent.Suggest("private", 10)
	if len(got) != 0 {
		t.Fatalf("account query leaked into public suggestions: %v", got)
	}
	got = recent.Suggest("public", 10)
	if len(got) != 1 || got[0] != "public query" {
		t.Fatalf("anonymous public query was not retained: %v", got)
	}
}
