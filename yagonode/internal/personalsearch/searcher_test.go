package personalsearch

import (
	"context"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

type fixedSearcher struct {
	response searchcore.Response
}

func (s fixedSearcher) Search(
	_ context.Context,
	request searchcore.Request,
) (searchcore.Response, error) {
	response := s.response
	response.Request = request
	return response, nil
}

func TestSearcherRequiresGlobalAndAccountOptIn(t *testing.T) {
	learning := openLearning(t, time.Now)
	principal := Principal{AccountID: "reader-opt-in", Personalization: true}
	if err := learning.RecordSearch(
		t.Context(),
		principal,
		"provincial archives history",
	); err != nil {
		t.Fatal(err)
	}
	response := searchcore.Response{Results: []searchcore.Result{
		{Title: "General page", URL: "https://general.example.test", Score: 1},
		{Title: "Provincial archives history", URL: "https://archive.example.test", Score: 0.9},
	}}
	inner := fixedSearcher{response: response}

	personalized := NewSearcher(inner, learning, func() bool { return true })
	got, err := personalized.Search(
		WithPrincipal(t.Context(), principal),
		searchcore.Request{Query: "history"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != len(response.Results) {
		t.Fatalf("personalized search changed the candidate count: %+v", got.Results)
	}
	var explained bool
	seen := map[string]bool{}
	for _, result := range got.Results {
		seen[result.URL] = true
		if result.URL == response.Results[1].URL && result.PersonalizedReason != "" &&
			result.Score > response.Results[1].Score {
			explained = true
		}
	}
	if !explained || !seen[response.Results[0].URL] || !seen[response.Results[1].URL] {
		t.Fatalf("opted-in user did not receive an explainable personal signal: %+v", got.Results)
	}

	guest, err := personalized.Search(t.Context(), searchcore.Request{Query: "history"})
	if err != nil {
		t.Fatal(err)
	}
	if guest.Results[0].URL != response.Results[0].URL ||
		guest.Results[0].PersonalizedReason != "" {
		t.Fatalf("guest results changed: %+v", guest.Results)
	}

	optedOut := principal
	optedOut.Personalization = false
	got, err = personalized.Search(
		WithPrincipal(t.Context(), optedOut),
		searchcore.Request{Query: "history"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Results[0].URL != response.Results[0].URL || got.Results[0].PersonalizedReason != "" {
		t.Fatalf("opted-out account results changed: %+v", got.Results)
	}

	globallyDisabled := NewSearcher(inner, learning, func() bool { return false })
	got, err = globallyDisabled.Search(
		WithPrincipal(t.Context(), principal),
		searchcore.Request{Query: "history"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Results[0].URL != response.Results[0].URL || got.Results[0].PersonalizedReason != "" {
		t.Fatalf("global disable changed the results: %+v", got.Results)
	}
}
