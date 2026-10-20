package yagonode

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/D4rk4/yago/yagonode/internal/boltvault"
	"github.com/D4rk4/yago/yagonode/internal/personalsearch"
	"github.com/D4rk4/yago/yagonode/internal/tavilyapi"
	"github.com/D4rk4/yago/yagonode/internal/yacysearch"
)

type personalClickAggregateStub struct {
	prepared int
	clicked  int
	clickKey string
}

type personalClickFixture struct {
	accounts  *personalsearch.Accounts
	learning  *personalsearch.Learning
	aggregate *personalClickAggregateStub
	recorder  yacysearch.ImpressionRecorder
	principal personalsearch.Principal
	ctx       context.Context
	candidate yacysearch.ImpressionCandidate
	setEnable func(bool)
}

func (s *personalClickAggregateStub) PrepareImpression(
	_ context.Context,
	_ string,
	candidates []yacysearch.ImpressionCandidate,
) (yacysearch.PreparedImpression, error) {
	s.prepared++
	return yacysearch.PreparedImpression{
		Token: "aggregate-token",
		Order: identityOrder(len(candidates)),
	}, nil
}

func (s *personalClickAggregateStub) RecordClick(
	_ context.Context,
	token, _ string,
	_ int,
) error {
	s.clicked++
	s.clickKey = token
	return nil
}

func TestPersonalClickEnvelopePreservesAggregateCaptureAndAccountIsolation(t *testing.T) {
	fixture := newPersonalClickFixture(t)
	prepared, err := fixture.recorder.PrepareImpression(
		fixture.ctx,
		"archive history",
		[]yacysearch.ImpressionCandidate{fixture.candidate},
	)
	if err != nil || prepared.Token == "" {
		t.Fatalf("prepared combined impression = %+v, %v", prepared, err)
	}
	aggregateToken, personalToken, wrapped := splitImpressionToken(prepared.Token)
	if !wrapped || aggregateToken != "aggregate-token" || personalToken == "" {
		t.Fatalf("combined token components = %q, %q, %v", aggregateToken, personalToken, wrapped)
	}
	if err := fixture.recorder.RecordClick(
		fixture.ctx,
		prepared.Token,
		fixture.candidate.URLIdentity,
		fixture.candidate.Position,
	); err != nil {
		t.Fatal(err)
	}
	if fixture.aggregate.clicked != 1 || fixture.aggregate.clickKey != "aggregate-token" {
		t.Fatalf("legacy aggregate click was not preserved: %+v", fixture.aggregate)
	}
	rows, err := fixture.learning.History(t.Context(), fixture.principal.AccountID, 10)
	if err != nil || len(rows) != 1 || rows[0].Kind != "click" {
		t.Fatalf("personal click history = %+v, %v", rows, err)
	}
	otherRows, err := fixture.learning.History(t.Context(), "account-b", 10)
	if err != nil || len(otherRows) != 0 {
		t.Fatalf("click leaked to another account: %+v, %v", otherRows, err)
	}
}

func TestPersonalClickIsIgnoredWhenOperatorDisablesLearning(t *testing.T) {
	fixture := newPersonalClickFixture(t)
	prepared, err := fixture.recorder.PrepareImpression(
		fixture.ctx,
		"archive history",
		[]yacysearch.ImpressionCandidate{fixture.candidate},
	)
	if err != nil || prepared.Token == "" {
		t.Fatalf("prepare impression before operator disable = %+v, %v", prepared, err)
	}
	fixture.setEnable(false)
	if err := fixture.recorder.RecordClick(
		fixture.ctx,
		prepared.Token,
		fixture.candidate.URLIdentity,
		fixture.candidate.Position,
	); err != nil {
		t.Fatal(err)
	}
	rows, err := fixture.learning.History(t.Context(), fixture.principal.AccountID, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("personal click was recorded after global disable: %+v, %v", rows, err)
	}
}

func TestPersonalTavilyImpressionCanRecordAgentClick(t *testing.T) {
	fixture := newPersonalClickFixture(t)
	feedback := newPersonalTavilySearchFeedbackRecorder(publicSearchAssembly{
		personalLearning:       fixture.learning,
		personalizationEnabled: func() bool { return true },
	})
	token := feedback.PrepareSearchImpression(
		fixture.ctx,
		"archive history",
		[]tavilyapi.SearchResult{{URL: fixture.candidate.URLIdentity}},
	)
	if token == "" {
		t.Fatal("opted-in agent search did not receive a click-feedback token")
	}
	if err := fixture.recorder.RecordClick(
		fixture.ctx,
		token,
		fixture.candidate.URLIdentity,
		1,
	); err != nil {
		t.Fatal(err)
	}
	rows, err := fixture.learning.History(t.Context(), fixture.principal.AccountID, 10)
	if err != nil || len(rows) != 1 || rows[0].Kind != "click" {
		t.Fatalf("agent click feedback history = %+v, %v", rows, err)
	}
}

func TestGuestImpressionDoesNotCarryPersonalState(t *testing.T) {
	fixture := newPersonalClickFixture(t)
	prepared, err := fixture.recorder.PrepareImpression(
		t.Context(),
		"archive history",
		[]yacysearch.ImpressionCandidate{fixture.candidate},
	)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, personal, wrapped := splitImpressionToken(prepared.Token)
	if wrapped || personal != "" || aggregate != prepared.Token || prepared.Token == "" {
		t.Fatalf(
			"guest token unexpectedly carries personal state: %q, %q, %v",
			aggregate,
			personal,
			wrapped,
		)
	}
}

func newPersonalClickFixture(t *testing.T) personalClickFixture {
	t.Helper()
	storage, err := boltvault.Open(filepath.Join(t.TempDir(), "state.db"), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	accounts, err := personalsearch.NewAccounts(storage, personalsearch.AccountsConfig{})
	if err != nil {
		t.Fatal(err)
	}
	learning, err := personalsearch.NewLearning(storage, personalsearch.LearningConfig{})
	if err != nil {
		t.Fatal(err)
	}
	aggregate := &personalClickAggregateStub{}
	enabled := true
	assembly := publicSearchAssembly{
		clickCapture: true, clickRecorder: aggregate,
		personalAccounts: accounts, personalLearning: learning,
		personalizationEnabled: func() bool { return enabled },
	}
	recorder := newPersonalYacyImpressionRecorder(assembly)
	principal := personalsearch.Principal{AccountID: "account-a", Personalization: true}
	return personalClickFixture{
		accounts: accounts, learning: learning, aggregate: aggregate,
		recorder:  recorder,
		principal: principal, ctx: personalsearch.WithPrincipal(t.Context(), principal),
		candidate: yacysearch.ImpressionCandidate{
			URLIdentity: "https://archive.example.test/item", Position: 1,
		},
		setEnable: func(value bool) { enabled = value },
	}
}
