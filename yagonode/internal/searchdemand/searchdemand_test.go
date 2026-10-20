package searchdemand

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/memvault"
	"github.com/D4rk4/yago/yagonode/internal/vault"
)

func openTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	v, err := memvault.Open(0)
	if err != nil {
		t.Fatalf("memvault.Open: %v", err)
	}
	t.Cleanup(func() { _ = v.Close() })
	store, err := Open(v)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	return store, context.Background()
}

func recordInput(
	query, client string,
	authenticated bool,
	at time.Time,
	cfg Config,
) RecordSearchInput {
	return RecordSearchInput{
		Query: query, ClientAddress: client, Authenticated: authenticated, At: at, Config: cfg,
	}
}

type reservationTestInput struct {
	store  *Store
	ctx    context.Context
	topic  string
	day    string
	runID  string
	pages  uint64
	wanted ReservationState
}

func TestRecordSearchAppliesClientAndTopicQuotas(t *testing.T) {
	store, ctx := openTestStore(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	cfg.Enabled = true
	var topic Topic
	for range cfg.PerTopicDailyCredits {
		var accepted bool
		var err error
		topic, accepted, err = store.RecordSearch(
			ctx, recordInput("  OWEN   SOUND ", "192.0.2.44", false, now, cfg),
		)
		if err != nil || !accepted {
			t.Fatalf("accepted search = %v, err = %v", accepted, err)
		}
	}
	if topic.Query != "owen sound" || topic.ID == "" {
		t.Fatalf("topic = %+v", topic)
	}
	if len(topic.Events) != 1 || topic.Events[0].Count != cfg.PerTopicDailyCredits {
		t.Fatalf("same-day topic events = %+v, want one aggregated daily bucket", topic.Events)
	}
	if _, accepted, err := store.RecordSearch(
		ctx, recordInput("owen sound", "192.0.2.44", false, now, cfg),
	); err != nil || accepted {
		t.Fatalf("per-topic cap: accepted=%v err=%v", accepted, err)
	}
	for index := uint32(0); index < cfg.AnonymousDailyCredits; index++ {
		query := "topic " + string(rune('a'+index))
		if _, accepted, err := store.RecordSearch(
			ctx, recordInput(query, "198.51.100.20", false, now, cfg),
		); err != nil ||
			!accepted {
			t.Fatalf("anonymous quota unexpectedly rejected: accepted=%v err=%v", accepted, err)
		}
	}
	if _, accepted, err := store.RecordSearch(
		ctx, recordInput("last topic", "198.51.100.20", false, now, cfg),
	); err != nil || accepted {
		t.Fatalf("anonymous daily cap: accepted=%v err=%v", accepted, err)
	}
	if _, accepted, err := store.RecordSearch(
		ctx, recordInput("owen sound", "192.0.2.44", false, now.Add(24*time.Hour), cfg),
	); err != nil || !accepted {
		t.Fatalf("next UTC day should reset quota: accepted=%v err=%v", accepted, err)
	}
}

func TestRecordSearchBoundsRetainedTopicCount(t *testing.T) {
	store, ctx := openTestStore(t)
	now := time.Now().UTC()
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.MaximumTopics = 1
	if _, accepted, err := store.RecordSearch(
		ctx, recordInput("first topic", "192.0.2.1", false, now, cfg),
	); err != nil ||
		!accepted {
		t.Fatalf("first topic: accepted=%v err=%v", accepted, err)
	}
	if _, accepted, err := store.RecordSearch(
		ctx, recordInput("second topic", "192.0.2.2", false, now, cfg),
	); err != nil ||
		accepted {
		t.Fatalf("topic-cap overflow: accepted=%v err=%v", accepted, err)
	}
}

func TestRecordSearchBoundsRetainedClientDays(t *testing.T) {
	store, ctx := openTestStore(t)
	now := time.Now().UTC()
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.MaximumClientDays = 1
	if _, accepted, err := store.RecordSearch(
		ctx, recordInput("shared topic", "192.0.2.1", false, now, cfg),
	); err != nil ||
		!accepted {
		t.Fatalf("first client day: accepted=%v err=%v", accepted, err)
	}
	if _, accepted, err := store.RecordSearch(
		ctx, recordInput("shared topic", "192.0.2.2", false, now, cfg),
	); err != nil ||
		accepted {
		t.Fatalf("client-day cap overflow: accepted=%v err=%v", accepted, err)
	}
}

func TestRecordSearchIsDisabledAndRejectsInvalidQueries(t *testing.T) {
	store, ctx := openTestStore(t)
	now := time.Now().UTC()
	cfg := DefaultConfig()
	if _, accepted, err := store.RecordSearch(
		ctx, recordInput("secret query", "192.0.2.1", false, now, cfg),
	); err != nil ||
		accepted {
		t.Fatalf("disabled feature accepted=%v err=%v", accepted, err)
	}
	cfg.Enabled = true
	for _, query := range []string{"", "  ", string(make([]byte, 257))} {
		if _, accepted, err := store.RecordSearch(
			ctx, recordInput(query, "192.0.2.1", false, now, cfg),
		); err != nil ||
			accepted {
			t.Fatalf("invalid query %q accepted=%v err=%v", query, accepted, err)
		}
	}
	if _, accepted, err := store.RecordSearch(
		ctx, recordInput("valid query", "", false, now, cfg),
	); err != nil ||
		accepted {
		t.Fatalf("missing client address accepted=%v err=%v", accepted, err)
	}
}

func TestClientPseudonymIsKeyedAndRotatesDaily(t *testing.T) {
	store, _ := openTestStore(t)
	address := "192.0.2.23"
	firstDay := store.clientID("2026-10-20", address)
	secondDay := store.clientID("2026-10-21", address)
	if firstDay == address || secondDay == address || firstDay == secondDay {
		t.Fatalf("client pseudonyms do not rotate as expected: %q %q", firstDay, secondDay)
	}
}

func TestAuthenticatedClientHasIndependentHigherQuota(t *testing.T) {
	store, ctx := openTestStore(t)
	now := time.Now().UTC()
	cfg := DefaultConfig()
	cfg.Enabled = true
	for i := uint32(0); i < cfg.AuthenticatedDailyCredits; i++ {
		query := "q " + string(rune('a'+i%26)) + " " + string(rune('A'+i/26))
		if _, accepted, err := store.RecordSearch(
			ctx, recordInput(query, "192.0.2.8", true, now, cfg),
		); err != nil ||
			!accepted {
			t.Fatalf("authenticated quota rejected at %d: accepted=%v err=%v", i, accepted, err)
		}
	}
	if _, accepted, err := store.RecordSearch(
		ctx, recordInput("overflow", "192.0.2.8", true, now, cfg),
	); err != nil ||
		accepted {
		t.Fatalf("authenticated daily cap: accepted=%v err=%v", accepted, err)
	}
}

func TestTopicWeightDecaysByHalfLife(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	topic := Topic{Events: []Event{{At: now, Count: 1}}}
	if got := TopicWeight(topic, now, 7*24*time.Hour); got < 0.999999 || got > 1.000001 {
		t.Fatalf("initial weight = %f", got)
	}
	if got := TopicWeight(
		topic,
		now.Add(7*24*time.Hour),
		7*24*time.Hour,
	); got < 0.499999 ||
		got > 0.500001 {
		t.Fatalf("one-half-life weight = %f", got)
	}
}

func TestTopicsIncludeCurrentDayBucketBeforeNoon(t *testing.T) {
	store, ctx := openTestStore(t)
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	cfg.Enabled = true
	topic, accepted, err := store.RecordSearch(
		ctx, recordInput("morning topic", "192.0.2.5", false, now, cfg),
	)
	if err != nil || !accepted {
		t.Fatalf("RecordSearch: accepted=%v err=%v", accepted, err)
	}
	topics, err := store.Topics(ctx, now, cfg.Retention)
	if err != nil || len(topics) != 1 || topics[0].ID != topic.ID {
		t.Fatalf("Topics before noon = %+v, err=%v", topics, err)
	}
	if weight := TopicWeight(
		topics[0],
		now,
		cfg.DecayHalfLife,
	); weight < 0.999999 ||
		weight > 1.000001 {
		t.Fatalf("morning event weight = %f, want one", weight)
	}
}

func TestAllocateSplitsDemandAndKeepsNoveltyProbeBounded(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	topics := []Topic{
		{ID: "a", Query: "topic a", Events: []Event{{At: now, Count: 1}}},
		{ID: "b", Query: "topic b", Events: []Event{{At: now, Count: 1}}},
		{ID: "c", Query: "topic c", Events: []Event{{At: now, Count: 1}}},
	}
	budgets := Allocate(topics, 900, 0, now, cfg)
	if len(budgets) != 3 {
		t.Fatalf("budgets = %+v", budgets)
	}
	for _, budget := range budgets {
		if budget.Pages != 25 || !budget.ProbeOnly {
			t.Fatalf("first-use budget = %+v", budget)
		}
	}
	for i := range topics {
		topics[i].UsefulDocuments = 1
	}
	budgets = Allocate(topics, 900, 0, now, cfg)
	if len(budgets) != 3 {
		t.Fatalf("evidence-backed budgets = %+v", budgets)
	}
	for _, budget := range budgets {
		if budget.Pages != 150 || budget.ProbeOnly {
			t.Fatalf("equal demand share = %+v, want 150", budget)
		}
	}
}

func TestAllocateCapsSingleTopicAndRespectsReservedCapacity(t *testing.T) {
	now := time.Now().UTC()
	cfg := DefaultConfig()
	topic := []Topic{
		{ID: "one", Query: "one", UsefulDocuments: 1, Events: []Event{{At: now, Count: 1}}},
	}
	got := Allocate(topic, 1000, 0, now, cfg)
	if len(got) != 1 || got[0].Pages != 250 {
		t.Fatalf("single topic cap = %+v, want 250 pages", got)
	}
	if got := Allocate(topic, 1000, 800, now, cfg); len(got) != 1 || got[0].Pages != 100 {
		t.Fatalf("reserved capacity allocation = %+v, want 100", got)
	}
	if got := Allocate(topic, 1000, 1000, now, cfg); len(got) != 0 {
		t.Fatalf("fully reserved capacity allocated: %+v", got)
	}
}

func TestAllocateRedistributesUnusedProbeCapacity(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	topics := []Topic{
		{
			ID:            "probe-exhausted",
			Query:         "new topic",
			Events:        []Event{{At: now, Count: 1}},
			ProbeAttempts: 25,
		},
		{
			ID:              "source-a",
			Query:           "known source a",
			Events:          []Event{{At: now, Count: 1}},
			UsefulDocuments: 1,
		},
		{
			ID:              "source-b",
			Query:           "known source b",
			Events:          []Event{{At: now, Count: 1}},
			UsefulDocuments: 1,
		},
	}
	budgets := Allocate(topics, 900, 0, now, cfg)
	if len(budgets) != 2 {
		t.Fatalf("budgets = %+v, want only evidence-backed topics", budgets)
	}
	for _, budget := range budgets {
		if budget.Pages != 225 {
			t.Fatalf("redistributed budget = %+v, want 225 pages", budget)
		}
	}
}

func TestRunSnapshotsAreIdempotentAndReleaseReservation(t *testing.T) {
	store, ctx := openTestStore(t)
	now := time.Now().UTC()
	cfg := DefaultConfig()
	cfg.Enabled = true
	topic, accepted, err := store.RecordSearch(
		ctx, recordInput("city budget", "192.0.2.2", false, now, cfg),
	)
	if err != nil || !accepted {
		t.Fatalf("RecordSearch: accepted=%v err=%v", accepted, err)
	}
	day := now.UTC().Format("2006-01-02")
	assertReservationState(t, reservationTestInput{
		store: store, ctx: ctx, topic: topic.ID, day: day, runID: "reservation-1",
		pages: 50, wanted: ReservationCreated,
	})
	assertReservationState(t, reservationTestInput{
		store: store, ctx: ctx, topic: topic.ID, day: day, runID: "reservation-1",
		pages: 50, wanted: ReservationResumed,
	})
	assertReservationState(t, reservationTestInput{
		store: store, ctx: ctx, topic: topic.ID, day: day, runID: "reservation-other",
		pages: 50, wanted: ReservationBusy,
	})
	progressAt := now.Add(time.Hour)
	if err := store.RecordRunSnapshot(ctx, RunSnapshotInput{
		RunID: "run-1", ReservationID: "reservation-1", Attempts: 10,
		Indexed: 2, At: progressAt,
	}); err != nil {
		t.Fatalf("RecordRunSnapshot: %v", err)
	}
	if err := store.RecordRunSnapshot(ctx, RunSnapshotInput{
		RunID: "run-1", ReservationID: "reservation-1", Attempts: 10,
		Indexed: 2, At: progressAt,
	}); err != nil {
		t.Fatalf("duplicate progress: %v", err)
	}
	current, err := store.Topic(ctx, topic.ID)
	if err != nil || current.DailyAttempts[day] != 10 || current.DailyReserved[day] != 40 ||
		current.UsefulDocuments != 2 {
		t.Fatalf("after progress = %+v, err=%v", current, err)
	}
	if err := store.RecordRunSnapshot(ctx, RunSnapshotInput{
		RunID: "run-1", ReservationID: "reservation-1", Attempts: 20,
		Indexed: 3, Terminal: true, At: progressAt,
	}); err != nil {
		t.Fatalf("terminal progress: %v", err)
	}
	current, err = store.Topic(ctx, topic.ID)
	if err != nil || current.DailyAttempts[day] != 20 || current.DailyReserved[day] != 0 ||
		current.UsefulDocuments != 3 {
		t.Fatalf("after terminal progress = %+v, err=%v", current, err)
	}
	if !current.LastSeen.Equal(now) {
		t.Fatalf(
			"crawler activity extended search-topic retention: last seen %v, want %v",
			current.LastSeen,
			now,
		)
	}
	assertReservationState(t, reservationTestInput{
		store: store, ctx: ctx, topic: topic.ID, day: day, runID: "reservation-next",
		pages: 5, wanted: ReservationCreated,
	})
	if err := store.ReleaseReservation(ctx, "reservation-next"); err != nil {
		t.Fatalf("ReleaseReservation: %v", err)
	}
	capacity, err := store.MeasuredDailyCapacity(ctx, now.Add(24*time.Hour))
	if err != nil || capacity != 20 {
		t.Fatalf("measured capacity = %d, err=%v; want 20", capacity, err)
	}
}

func assertReservationState(t *testing.T, input reservationTestInput) {
	t.Helper()
	state, err := input.store.Reserve(input.ctx, input.topic, input.day, input.runID, input.pages)
	if err != nil || state != input.wanted {
		t.Fatalf("Reserve(%q): state=%v err=%v, want %v", input.runID, state, err, input.wanted)
	}
}

func TestPruneTrimsRollingTopicCounters(t *testing.T) {
	store, ctx := openTestStore(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	cfg.Enabled = true
	topic, accepted, err := store.RecordSearch(
		ctx, recordInput("library records", "192.0.2.9", false, now, cfg),
	)
	if err != nil || !accepted {
		t.Fatalf("RecordSearch: accepted=%v err=%v", accepted, err)
	}
	if err := store.vault.Update(ctx, func(tx *vault.Txn) error {
		stored, found, err := store.topics.Get(tx, vault.Key(topic.ID))
		if err != nil {
			return fmt.Errorf("load topic to seed old counter: %w", err)
		}
		if !found {
			return fmt.Errorf("topic %q was not found", topic.ID)
		}
		stored.DailyAttempts = make(map[string]uint64)
		stored.DailyAttempts["2026-09-01"] = 4
		stored.DailyAttempts["2026-10-20"] = 2
		return store.topics.Put(tx, vault.Key(topic.ID), stored)
	}); err != nil {
		t.Fatalf("seed old daily counter: %v", err)
	}
	if err := store.Prune(ctx, now, 28*24*time.Hour); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	stored, err := store.Topic(ctx, topic.ID)
	if err != nil {
		t.Fatalf("Topic: %v", err)
	}
	if _, exists := stored.DailyAttempts["2026-09-01"]; exists {
		t.Fatal("expired daily counter remains")
	}
	if stored.DailyAttempts["2026-10-20"] != 2 {
		t.Fatalf("current daily counter = %d, want 2", stored.DailyAttempts["2026-10-20"])
	}
}
