package yagonode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/D4rk4/yago/yagonode/internal/personalsearch"
	"github.com/D4rk4/yago/yagonode/internal/publicportal"
	"github.com/D4rk4/yago/yagonode/internal/yacysearch"
)

const personalImpressionPrefix = "yp1."

type personalImpressionEnvelope struct {
	Aggregate string `json:"a,omitempty"`
	Personal  string `json:"p"`
}

type personalYacyImpressionRecorder struct {
	aggregate yacysearch.ImpressionRecorder
	personalImpressionPolicy
}

type personalPortalImpressionRecorder struct {
	aggregate yacysearch.ImpressionRecorder
	personalImpressionPolicy
}

type personalImpressionPolicy struct {
	accounts *personalsearch.Accounts
	learning *personalsearch.Learning
	enabled  func() bool
}

func newPersonalYacyImpressionRecorder(
	assembly publicSearchAssembly,
) yacysearch.ImpressionRecorder {
	aggregate := assembly.clickRecorder
	if !assembly.clickCapture {
		aggregate = nil
	}
	if assembly.personalLearning == nil {
		return aggregate
	}
	return personalYacyImpressionRecorder{
		aggregate: aggregate,
		personalImpressionPolicy: personalImpressionPolicy{
			accounts: assembly.personalAccounts,
			learning: assembly.personalLearning,
			enabled:  assembly.personalizationEnabled,
		},
	}
}

func newPersonalPortalImpressionRecorder(
	assembly publicSearchAssembly,
) publicportal.ImpressionRecorder {
	aggregate := assembly.clickRecorder
	if !assembly.clickCapture {
		aggregate = nil
	}
	if assembly.personalLearning == nil {
		if assembly.clickCapture {
			return assembly.portalClickRecorder
		}
		return nil
	}
	return personalPortalImpressionRecorder{
		aggregate: aggregate,
		personalImpressionPolicy: personalImpressionPolicy{
			accounts: assembly.personalAccounts,
			learning: assembly.personalLearning,
			enabled:  assembly.personalizationEnabled,
		},
	}
}

func (r personalYacyImpressionRecorder) PrepareImpression(
	ctx context.Context,
	query string,
	candidates []yacysearch.ImpressionCandidate,
) (yacysearch.PreparedImpression, error) {
	prepared, err := prepareAggregateImpression(ctx, r.aggregate, query, candidates)
	if err != nil {
		return yacysearch.PreparedImpression{}, fmt.Errorf(
			"prepare aggregate search impression: %w",
			err,
		)
	}
	personalToken, err := r.preparePersonal(ctx, query, candidates, prepared.Order)
	if err != nil {
		return preserveAggregateImpression(prepared)
	}
	prepared.Token = combineImpressionTokens(prepared.Token, personalToken)

	return prepared, nil
}

func (r personalYacyImpressionRecorder) RecordClick(
	ctx context.Context,
	token, urlIdentity string,
	position int,
) error {
	aggregateToken, personalToken, wrapped := splitImpressionToken(token)
	principal, authenticated := personalsearch.PrincipalFromContext(ctx)
	if !wrapped && authenticated && strings.HasPrefix(token, principal.AccountID+".") {
		personalToken = token
		aggregateToken = ""
	}
	if personalToken != "" && authenticated && r.learning != nil &&
		personalsearch.PersonalizationEnabled(r.enabled) {
		_ = r.learning.RecordClick(ctx, principal, personalToken, urlIdentity, position)
	}
	if aggregateToken != "" && r.aggregate != nil {
		if err := r.aggregate.RecordClick(ctx, aggregateToken, urlIdentity, position); err != nil {
			return fmt.Errorf("record aggregate search click: %w", err)
		}
	}

	return nil
}

func (r personalPortalImpressionRecorder) PrepareImpression(
	ctx context.Context,
	query string,
	candidates []publicportal.ImpressionCandidate,
) (publicportal.PreparedImpression, error) {
	converted := make([]yacysearch.ImpressionCandidate, len(candidates))
	for index, candidate := range candidates {
		converted[index] = yacysearch.ImpressionCandidate{
			URLIdentity: candidate.URLIdentity, ClusterIdentity: candidate.ClusterIdentity,
			Position: candidate.Position, LexicalPosition: candidate.LexicalPosition,
		}
	}
	prepared, err := prepareAggregateImpression(ctx, r.aggregate, query, converted)
	if err != nil {
		return publicportal.PreparedImpression{}, fmt.Errorf(
			"prepare aggregate portal impression: %w",
			err,
		)
	}
	personalToken, err := r.preparePersonal(ctx, query, converted, prepared.Order)
	if err != nil {
		return preservePortalAggregateImpression(prepared)
	}
	order := append([]int(nil), prepared.Order...)
	preparedToken := combineImpressionTokens(prepared.Token, personalToken)

	return publicportal.PreparedImpression{Token: preparedToken, Order: order}, nil
}

func preserveAggregateImpression(
	prepared yacysearch.PreparedImpression,
) (yacysearch.PreparedImpression, error) {
	return prepared, nil
}

func preservePortalAggregateImpression(
	prepared yacysearch.PreparedImpression,
) (publicportal.PreparedImpression, error) {
	return publicportal.PreparedImpression{
		Token: prepared.Token,
		Order: append([]int(nil), prepared.Order...),
	}, nil
}

func (r personalPortalImpressionRecorder) preparePersonal(
	ctx context.Context,
	query string,
	candidates []yacysearch.ImpressionCandidate,
	order []int,
) (string, error) {
	return r.prepare(ctx, query, candidates, order)
}

func (r personalYacyImpressionRecorder) preparePersonal(
	ctx context.Context,
	query string,
	candidates []yacysearch.ImpressionCandidate,
	order []int,
) (string, error) {
	return r.prepare(ctx, query, candidates, order)
}

func prepareAggregateImpression(
	ctx context.Context,
	recorder yacysearch.ImpressionRecorder,
	query string,
	candidates []yacysearch.ImpressionCandidate,
) (yacysearch.PreparedImpression, error) {
	if recorder != nil {
		prepared, err := recorder.PrepareImpression(ctx, query, candidates)
		if err != nil {
			return yacysearch.PreparedImpression{}, fmt.Errorf(
				"prepare aggregate search impression: %w",
				err,
			)
		}
		if validPersonalOrder(prepared.Order, len(candidates)) {
			return prepared, nil
		}
		return yacysearch.PreparedImpression{Order: identityOrder(len(candidates))}, nil
	}

	return yacysearch.PreparedImpression{Order: identityOrder(len(candidates))}, nil
}

func (p personalImpressionPolicy) prepare(
	ctx context.Context,
	query string,
	candidates []yacysearch.ImpressionCandidate,
	order []int,
) (string, error) {
	if p.accounts == nil || p.learning == nil || !personalsearch.PersonalizationEnabled(p.enabled) {
		return "", nil
	}
	principal, authenticated := personalsearch.PrincipalFromContext(ctx)
	if !authenticated || !principal.Personalization || !validPersonalOrder(order, len(candidates)) {
		return "", nil
	}
	displayed := make([]personalsearch.ImpressionCandidate, len(order))
	firstPosition := 1
	if len(candidates) > 0 {
		firstPosition = candidates[0].Position
	}
	for index, original := range order {
		displayed[index] = personalsearch.ImpressionCandidate{
			URLIdentity: candidates[original].URLIdentity,
			Position:    firstPosition + index,
			Rank:        index + 1,
		}
	}

	token, err := p.learning.PrepareImpression(ctx, principal, query, displayed)
	if err != nil {
		return "", fmt.Errorf("prepare account search impression: %w", err)
	}
	return token, nil
}

func combineImpressionTokens(aggregateToken, personalToken string) string {
	if personalToken == "" {
		return aggregateToken
	}
	envelope, err := json.Marshal(
		personalImpressionEnvelope{Aggregate: aggregateToken, Personal: personalToken},
	)
	if err != nil {
		return aggregateToken
	}
	wrapped := personalImpressionPrefix + base64.RawURLEncoding.EncodeToString(envelope)
	if len(wrapped) > 65536 {
		return aggregateToken
	}
	return wrapped
}

func splitImpressionToken(token string) (string, string, bool) {
	if len(token) > 65536 || len(token) < len(personalImpressionPrefix) ||
		token[:len(personalImpressionPrefix)] != personalImpressionPrefix {
		return token, "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(token[len(personalImpressionPrefix):])
	if err != nil {
		return token, "", false
	}
	var envelope personalImpressionEnvelope
	if json.Unmarshal(raw, &envelope) != nil || envelope.Personal == "" ||
		len(envelope.Personal) > 256 {
		return token, "", false
	}

	return envelope.Aggregate, envelope.Personal, true
}

func validPersonalOrder(order []int, length int) bool {
	if len(order) != length {
		return false
	}
	seen := make([]bool, length)
	for _, index := range order {
		if index < 0 || index >= length || seen[index] {
			return false
		}
		seen[index] = true
	}
	return true
}

func identityOrder(length int) []int {
	order := make([]int, length)
	for index := range order {
		order[index] = index
	}
	return order
}

func mountPersonalSearchPages(
	mux *http.ServeMux,
	accounts *personalsearch.Accounts,
	learning *personalsearch.Learning,
	globalEnabled func() bool,
) {
	if accounts != nil {
		accounts.MountUserPages(mux, learning, globalEnabled)
	}
}
