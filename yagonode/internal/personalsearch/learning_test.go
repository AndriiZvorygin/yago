package personalsearch

import (
	"testing"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/memvault"
	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

func openLearning(t *testing.T, now func() time.Time) *Learning {
	t.Helper()
	storage, err := memvault.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	learning, err := NewLearning(storage, LearningConfig{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return learning
}

func TestOptOutDoesNotRecordSearchOrImpression(t *testing.T) {
	learning := openLearning(t, time.Now)
	principal := Principal{AccountID: "reader-a"}
	if err := learning.RecordSearch(t.Context(), principal, "private query"); err != nil {
		t.Fatal(err)
	}
	token, err := learning.PrepareImpression(
		t.Context(),
		principal,
		"private query",
		[]ImpressionCandidate{{URLIdentity: "https://example.test", Position: 1}},
	)
	if err != nil || token != "" {
		t.Fatalf("opt-out impression = %q, %v", token, err)
	}
	rows, err := learning.History(t.Context(), principal.AccountID, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("opt-out history = %#v, %v", rows, err)
	}
}

func TestPersonalClickRequiresOwnerAndImpressedPosition(t *testing.T) {
	learning := openLearning(t, time.Now)
	owner := Principal{AccountID: "owner-a", Personalization: true}
	if err := learning.RecordSearch(t.Context(), owner, "local archive research"); err != nil {
		t.Fatal(err)
	}
	token, err := learning.PrepareImpression(
		t.Context(),
		owner,
		"local archive research",
		[]ImpressionCandidate{
			{URLIdentity: "https://archive.example.test/item", Position: 11, Rank: 1},
			{URLIdentity: "https://other.example.test/page", Position: 12, Rank: 2},
		},
	)
	if err != nil || token == "" {
		t.Fatalf("prepare impression = %q, %v", token, err)
	}
	if err := learning.RecordClick(
		t.Context(),
		Principal{AccountID: "owner-b", Personalization: true},
		token,
		"https://archive.example.test/item",
		11,
	); err == nil {
		t.Fatal("another account recorded a click on the owner's impression")
	}
	if err := learning.RecordClick(
		t.Context(),
		owner,
		token,
		"https://archive.example.test/item",
		12,
	); err == nil {
		t.Fatal("click accepted with a position that did not show this URL")
	}
	if err := learning.RecordClick(
		t.Context(),
		owner,
		token,
		"https://archive.example.test/item",
		11,
	); err != nil {
		t.Fatal(err)
	}
	if err := learning.RecordClick(
		t.Context(),
		owner,
		token,
		"https://archive.example.test/item",
		11,
	); err != nil {
		t.Fatalf("idempotent click retry: %v", err)
	}
	rows, err := learning.History(t.Context(), owner.AccountID, 20)
	if err != nil || len(rows) != 2 {
		t.Fatalf("owner history rows = %d, %v", len(rows), err)
	}
	profile, err := learning.Interests(t.Context(), owner.AccountID)
	if err != nil || profile.Hosts["archive.example.test"] <= 0 || profile.Terms["archive"] <= 0 {
		t.Fatalf("personal interest profile = %+v, %v", profile, err)
	}
}

func TestHistoryRetentionRemovesExpiredEventsAndImpressions(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	learning := openLearning(t, func() time.Time { return now })
	principal := Principal{AccountID: "reader-retention", Personalization: true}
	if err := learning.RecordSearch(t.Context(), principal, "retained query"); err != nil {
		t.Fatal(err)
	}
	token, err := learning.PrepareImpression(
		t.Context(),
		principal,
		"retained query",
		[]ImpressionCandidate{{URLIdentity: "https://archive.example.test", Position: 1}},
	)
	if err != nil || token == "" {
		t.Fatalf("prepare expiring impression: %v", err)
	}
	if removed, err := learning.PruneExpiredImpressions(
		t.Context(),
		10,
	); err != nil ||
		removed != 0 {
		t.Fatalf("fresh impressions pruned = %d, %v", removed, err)
	}
	now = now.Add(3 * time.Hour)
	if removed, err := learning.PruneExpiredImpressions(
		t.Context(),
		10,
	); err != nil ||
		removed != 1 {
		t.Fatalf("expired impressions pruned = %d, %v", removed, err)
	}
	now = now.Add(91 * 24 * time.Hour)
	if removed, err := learning.PruneExpired(t.Context(), 10); err != nil || removed != 1 {
		t.Fatalf("expired history rows pruned = %d, %v", removed, err)
	}
	rows, err := learning.History(t.Context(), principal.AccountID, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("history after retention = %#v, %v", rows, err)
	}
	if err := learning.ClearHistory(t.Context(), principal.AccountID); err != nil {
		t.Fatal(err)
	}
	if token, err := learning.PrepareImpression(
		t.Context(),
		principal,
		"after cleanup",
		[]ImpressionCandidate{{URLIdentity: "https://archive.example.test/new", Position: 1}},
	); err != nil ||
		token == "" {
		t.Fatalf("new impression after cleanup = %q, %v", token, err)
	}
}

func TestPersonalizedSearcherUsesOnlyOptedInAccountAndKeepsCandidateSet(t *testing.T) {
	learning := openLearning(t, time.Now)
	principal := Principal{AccountID: "reader-ranking", Personalization: true}
	if err := learning.RecordSearch(
		t.Context(),
		principal,
		"provincial archives history",
	); err != nil {
		t.Fatal(err)
	}
	if err := learning.RecordClick(
		t.Context(),
		principal,
		"invalid.impression",
		"https://example.test",
		1,
	); err == nil {
		t.Fatal("click without an impression unexpectedly succeeded")
	}
	profile, err := learning.Interests(t.Context(), principal.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	response := searchcore.Response{Results: []searchcore.Result{
		{Title: "General result", URL: "https://general.example.test/page", Score: 1},
		{
			Title: "Provincial archives history",
			URL:   "https://archive.example.test/item",
			Score: 0.9,
		},
	}}
	ranked := rerankForAccount(response, profile)
	if len(ranked.Results) != 2 {
		t.Fatalf("personalized result ordering = %+v", ranked.Results)
	}
	var explained bool
	for _, result := range ranked.Results {
		if result.URL == response.Results[1].URL && result.PersonalizedReason != "" &&
			result.Score > response.Results[1].Score {
			explained = true
		}
	}
	if !explained {
		t.Fatalf(
			"single-query interest did not produce an explained score boost: %+v",
			ranked.Results,
		)
	}
	guest := rerankForAccount(
		response,
		InterestProfile{Terms: map[string]float64{}, Hosts: map[string]float64{}},
	)
	if guest.Results[0].URL != response.Results[0].URL ||
		guest.Results[0].PersonalizedReason != "" {
		t.Fatalf("empty profile changed guest order: %+v", guest.Results)
	}
}
