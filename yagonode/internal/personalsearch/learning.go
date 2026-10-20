package personalsearch

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/D4rk4/yago/yagonode/internal/searchcore"
	"github.com/D4rk4/yago/yagonode/internal/vault"
)

const (
	personalEventsBucket           vault.Name = "personalsearch_events"
	personalEventExpiryBucket      vault.Name = "personalsearch_event_expiry"
	personalEventCountsBucket      vault.Name = "personalsearch_event_counts"
	personalImpressionsBucket      vault.Name = "personalsearch_impressions"
	personalImpressionExpiryBucket vault.Name = "personalsearch_impression_expiry"
	personalImpressionCountsBucket vault.Name = "personalsearch_impression_counts"
	maximumPersonalEvents                     = 1000
	maximumPersonalImpressions                = 1000
	maximumPersonalHistoryPage                = maximumPersonalEvents
	maximumImpressionCandidates               = 50
	maximumPersonalQueryBytes                 = 512
	maximumPersonalURLBytes                   = 2048
	personalEventRetention                    = 90 * 24 * time.Hour
	personalInterestHalfLife                  = 7 * 24 * time.Hour
	personalImpressionLifetime                = 2 * time.Hour
	personalHistoryBoost                      = 0.0015
)

type personalEvent struct {
	ID        string    `json:"id"`
	AccountID string    `json:"accountId"`
	Kind      string    `json:"kind"`
	Query     string    `json:"query"`
	URL       string    `json:"url,omitempty"`
	Host      string    `json:"host,omitempty"`
	Position  int       `json:"position,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type HistoryEvent = personalEvent

type personalImpression struct {
	AccountID  string              `json:"accountId"`
	Query      string              `json:"query"`
	CreatedAt  time.Time           `json:"createdAt"`
	ExpiresAt  time.Time           `json:"expiresAt"`
	Candidates []personalCandidate `json:"candidates"`
	Clicked    map[string]bool     `json:"clicked,omitempty"`
}

type personalCandidate struct {
	URL      string `json:"url"`
	Position int    `json:"position"`
	Rank     int    `json:"rank"`
}

type LearningConfig struct {
	Now       func() time.Time
	Retention time.Duration
	HalfLife  time.Duration
}

type Learning struct {
	vault            *vault.Vault
	events           *vault.Collection[personalEvent]
	eventExpiry      *vault.Collection[string]
	eventCounts      *vault.Collection[int]
	impressions      *vault.Collection[personalImpression]
	impressionExpiry *vault.Collection[string]
	impressionCounts *vault.Collection[int]
	now              func() time.Time
	retention        time.Duration
	halfLife         time.Duration
}

type expiredPersonalImpression struct {
	key  string
	hash string
}

type InterestProfile struct {
	Terms map[string]float64
	Hosts map[string]float64
}

type ImpressionCandidate struct {
	URLIdentity string
	Position    int
	Rank        int
}

func NewLearning(storage *vault.Vault, config LearningConfig) (*Learning, error) {
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Retention <= 0 {
		config.Retention = personalEventRetention
	}
	if config.HalfLife <= 0 {
		config.HalfLife = personalInterestHalfLife
	}
	events, err := vault.Register(storage, personalEventsBucket, jsonRecordCodec[personalEvent]{})
	if err != nil {
		return nil, fmt.Errorf("register personal search history: %w", err)
	}
	eventExpiry, err := vault.Register(
		storage,
		personalEventExpiryBucket,
		jsonRecordCodec[string]{},
	)
	if err != nil {
		return nil, fmt.Errorf("register personal search history expiry index: %w", err)
	}
	eventCounts, err := vault.Register(storage, personalEventCountsBucket, jsonRecordCodec[int]{})
	if err != nil {
		return nil, fmt.Errorf("register personal search history counts: %w", err)
	}
	impressions, err := vault.Register(
		storage,
		personalImpressionsBucket,
		jsonRecordCodec[personalImpression]{},
	)
	if err != nil {
		return nil, fmt.Errorf("register personal search impressions: %w", err)
	}
	impressionExpiry, err := vault.Register(
		storage,
		personalImpressionExpiryBucket,
		jsonRecordCodec[string]{},
	)
	if err != nil {
		return nil, fmt.Errorf("register personal search impression expiry index: %w", err)
	}
	impressionCounts, err := vault.Register(
		storage,
		personalImpressionCountsBucket,
		jsonRecordCodec[int]{},
	)
	if err != nil {
		return nil, fmt.Errorf("register personal search impression counts: %w", err)
	}

	return &Learning{
		vault: storage, events: events, eventExpiry: eventExpiry,
		eventCounts: eventCounts, impressions: impressions,
		impressionExpiry: impressionExpiry, impressionCounts: impressionCounts,
		now:       config.Now,
		retention: config.Retention, halfLife: config.HalfLife,
	}, nil
}

func (l *Learning) RecordSearch(ctx context.Context, principal Principal, query string) error {
	query = strings.TrimSpace(query)
	if principal.AccountID == "" || !principal.Personalization || query == "" ||
		len(query) > maximumPersonalQueryBytes {
		return nil
	}
	return l.recordEvent(ctx, personalEvent{
		AccountID: principal.AccountID, Kind: "search", Query: query, CreatedAt: l.now(),
	})
}

func (l *Learning) PrepareImpression(
	ctx context.Context,
	principal Principal,
	query string,
	candidates []ImpressionCandidate,
) (string, error) {
	if principal.AccountID == "" || !principal.Personalization || len(candidates) == 0 ||
		len(candidates) > maximumImpressionCandidates ||
		len(query) > maximumPersonalQueryBytes {
		return "", nil
	}
	token, err := randomSecret(32)
	if err != nil {
		return "", err
	}
	now := l.now()
	identity := personalImpression{
		AccountID: principal.AccountID, Query: strings.TrimSpace(query),
		CreatedAt: now, ExpiresAt: now.Add(personalImpressionLifetime),
		Candidates: normalizeImpressionCandidates(candidates), Clicked: map[string]bool{},
	}
	if len(identity.Candidates) == 0 {
		return "", nil
	}
	storageKey := principal.AccountID + ":" + secretHash(token)
	indexKey := personalExpiryKeyTime(identity.ExpiresAt, storageKey)
	if err := l.vault.Update(ctx, func(tx *vault.Txn) error {
		count, found, err := l.impressionCounts.Get(tx, vault.Key(principal.AccountID))
		if err != nil {
			return wrapPersonalError("read impression count", err)
		}
		if found && count >= maximumPersonalImpressions {
			return errors.New("personal impression capacity reached")
		}
		if err := l.impressions.Put(tx, vault.Key(storageKey), identity); err != nil {
			return wrapPersonalError("store search impression", err)
		}
		if err := l.impressionExpiry.Put(tx, vault.Key(indexKey), storageKey); err != nil {
			return wrapPersonalError("index search impression expiry", err)
		}
		if err := l.impressionCounts.Put(tx, vault.Key(principal.AccountID), count+1); err != nil {
			return wrapPersonalError("increment impression count", err)
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("save personal search impression: %w", err)
	}

	return principal.AccountID + "." + token, nil
}

func normalizeImpressionCandidates(candidates []ImpressionCandidate) []personalCandidate {
	result := make([]personalCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		candidateURL := strings.TrimSpace(candidate.URLIdentity)
		if len(candidateURL) == 0 || len(candidateURL) > maximumPersonalURLBytes ||
			candidate.Position < 1 {
			continue
		}
		rank := candidate.Rank
		if rank < 1 {
			rank = len(result) + 1
		}
		result = append(result, personalCandidate{
			URL: candidateURL, Position: candidate.Position, Rank: rank,
		})
	}

	return result
}

func (l *Learning) RecordClick(
	ctx context.Context,
	principal Principal,
	token, resultURL string,
	position int,
) error {
	if principal.AccountID == "" || !principal.Personalization || token == "" || len(token) > 256 ||
		position < 1 ||
		len(resultURL) > maximumPersonalURLBytes {
		return nil
	}
	accountID, secret, validToken := strings.Cut(token, ".")
	if !validToken || accountID != principal.AccountID || secret == "" {
		return ErrCredentialInvalid
	}
	hash := accountID + ":" + secretHash(secret)
	err := l.vault.Update(ctx, func(tx *vault.Txn) error {
		return l.recordClickInTransaction(tx, principal, hash, resultURL, position)
	})
	if err != nil {
		if errors.Is(err, ErrCredentialInvalid) {
			return ErrCredentialInvalid
		}
		return fmt.Errorf("record personal search click: %w", err)
	}

	return nil
}

func (l *Learning) recordClickInTransaction(
	tx *vault.Txn,
	principal Principal,
	hash, resultURL string,
	position int,
) error {
	impression, found, err := l.impressions.Get(tx, vault.Key(hash))
	if err != nil {
		return wrapPersonalError("read click impression", err)
	}
	if !found || impression.AccountID != principal.AccountID ||
		!impression.ExpiresAt.After(l.now()) {
		return ErrCredentialInvalid
	}
	candidate, matched := impressionCandidate(impression, resultURL, position)
	if !matched {
		return ErrCredentialInvalid
	}
	clickID := secretHash(hash + "\x00" + candidate.URL)
	if impression.Clicked == nil {
		impression.Clicked = make(map[string]bool)
	}
	if impression.Clicked[clickID] {
		return nil
	}
	impression.Clicked[clickID] = true
	if err := l.impressions.Put(tx, vault.Key(hash), impression); err != nil {
		return wrapPersonalError("mark impression clicked", err)
	}
	event := personalEvent{
		ID: clickID, AccountID: principal.AccountID, Kind: "click",
		Query: impression.Query, URL: candidate.URL, Host: resultHost(candidate.URL),
		Position: candidate.Rank, CreatedAt: l.now(),
	}

	return l.putEvent(tx, event)
}

func impressionCandidate(
	impression personalImpression,
	resultURL string,
	position int,
) (personalCandidate, bool) {
	for _, candidate := range impression.Candidates {
		if candidate.URL == resultURL && candidate.Position == position {
			return candidate, true
		}
	}

	return personalCandidate{}, false
}

func (l *Learning) History(
	ctx context.Context,
	accountID string,
	limit int,
) ([]personalEvent, error) {
	if limit < 1 {
		limit = 50
	}
	if limit > maximumPersonalHistoryPage {
		limit = maximumPersonalHistoryPage
	}
	rows := make([]personalEvent, 0, maximumPersonalEvents)
	err := l.vault.View(ctx, func(tx *vault.Txn) error {
		return l.events.Scan(
			tx,
			vault.Key(accountID+"\x00"),
			func(_ vault.Key, event personalEvent) (bool, error) {
				rows = append(rows, event)
				return true, nil
			},
		)
	})
	if err != nil {
		return nil, fmt.Errorf("read personal search history: %w", err)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })
	if len(rows) > limit {
		rows = rows[:limit]
	}

	return rows, nil
}

func (l *Learning) ClearHistory(ctx context.Context, accountID string) error {
	if accountID == "" {
		return ErrAccountUnavailable
	}
	if err := l.vault.Update(ctx, func(tx *vault.Txn) error {
		return l.clearHistoryInTransaction(tx, accountID)
	}); err != nil {
		return fmt.Errorf("clear personal search history: %w", err)
	}

	return nil
}

func (l *Learning) clearHistoryInTransaction(tx *vault.Txn, accountID string) error {
	var eventKeys []vault.Key
	if err := l.events.Scan(
		tx,
		vault.Key(accountID+"\x00"),
		func(key vault.Key, _ personalEvent) (bool, error) {
			eventKeys = append(eventKeys, key)
			return true, nil
		},
	); err != nil {
		return wrapPersonalError("scan account search events", err)
	}
	for _, key := range eventKeys {
		if _, err := l.deleteEvent(tx, key); err != nil {
			return wrapPersonalError("delete account search event", err)
		}
	}
	var impressionKeys []vault.Key
	if err := l.impressions.Scan(
		tx,
		vault.Key(accountID+":"),
		func(key vault.Key, _ personalImpression) (bool, error) {
			impressionKeys = append(impressionKeys, key)
			return true, nil
		},
	); err != nil {
		return wrapPersonalError("scan account impressions", err)
	}
	for _, key := range impressionKeys {
		if err := l.deleteStoredImpression(tx, key); err != nil {
			return err
		}
	}
	if _, err := l.eventCounts.Delete(tx, vault.Key(accountID)); err != nil {
		return wrapPersonalError("delete account event count", err)
	}
	if _, err := l.impressionCounts.Delete(tx, vault.Key(accountID)); err != nil {
		return wrapPersonalError("delete account impression count", err)
	}

	return nil
}

func (l *Learning) deleteStoredImpression(tx *vault.Txn, key vault.Key) error {
	impression, found, err := l.impressions.Get(tx, key)
	if err != nil {
		return wrapPersonalError("read account impression", err)
	}
	if !found {
		return nil
	}

	return l.deleteImpression(tx, key, impression)
}

func (l *Learning) Interests(ctx context.Context, accountID string) (InterestProfile, error) {
	profile := InterestProfile{Terms: map[string]float64{}, Hosts: map[string]float64{}}
	err := l.vault.View(ctx, func(tx *vault.Txn) error {
		return l.events.Scan(
			tx,
			vault.Key(accountID+"\x00"),
			func(_ vault.Key, event personalEvent) (bool, error) {
				age := l.now().Sub(event.CreatedAt)
				if age < 0 {
					age = 0
				}
				decay := 0.5 * (float64(age) / float64(l.halfLife))
				weight := 0.2
				if event.Kind == "click" {
					weight = 0.8 / float64(max(event.Position, 1))
					if weight < 0.08 {
						weight = 0.08
					}
					profile.Hosts[event.Host] += weight * decayWeight(decay)
				}
				for _, term := range textTerms(event.Query) {
					profile.Terms[term] += weight * decayWeight(decay)
				}
				return true, nil
			},
		)
	})
	if err != nil {
		return InterestProfile{}, fmt.Errorf("derive personal search interests: %w", err)
	}
	for term, weight := range profile.Terms {
		profile.Terms[term] = 1 - (1 / (1 + weight))
	}
	for host, weight := range profile.Hosts {
		profile.Hosts[host] = 1 - (1 / (1 + weight))
	}

	return profile, nil
}

func (l *Learning) PruneExpired(ctx context.Context, batch int) (int, error) {
	if batch < 1 {
		batch = 250
	}
	cutoff := l.now().Add(-l.retention).UnixNano()
	var expired []string
	err := l.vault.View(ctx, func(tx *vault.Txn) error {
		return l.eventExpiry.Scan(tx, nil, func(key vault.Key, eventKey string) (bool, error) {
			timestamp, valid := timestampFromExpiryKey(key)
			if !valid || timestamp >= cutoff || len(expired) >= batch {
				return false, nil
			}
			expired = append(expired, eventKey)
			return true, nil
		})
	})
	if err != nil {
		return 0, fmt.Errorf("scan expired personal events: %w", err)
	}
	if len(expired) == 0 {
		return 0, nil
	}
	removed := 0
	if err := l.vault.Update(ctx, func(tx *vault.Txn) error {
		for _, key := range expired {
			didDelete, err := l.deleteEvent(tx, vault.Key(key))
			if err != nil {
				return wrapPersonalError("delete expired personal event", err)
			}
			if didDelete {
				removed++
			}
		}
		return nil
	}); err != nil {
		return 0, fmt.Errorf("prune expired personal events: %w", err)
	}

	return removed, nil
}

func (l *Learning) PruneExpiredImpressions(ctx context.Context, batch int) (int, error) {
	if batch < 1 {
		batch = 250
	}
	cutoff := l.now().UnixNano()
	var expired []expiredPersonalImpression
	err := l.vault.View(ctx, func(tx *vault.Txn) error {
		return l.impressionExpiry.Scan(tx, nil, func(key vault.Key, hash string) (bool, error) {
			timestamp, valid := timestampFromExpiryKey(key)
			if !valid || timestamp >= cutoff || len(expired) >= batch {
				return false, nil
			}
			expired = append(expired, expiredPersonalImpression{key: string(key), hash: hash})
			return true, nil
		})
	})
	if err != nil {
		return 0, fmt.Errorf("scan expired personal impressions: %w", err)
	}
	removed := 0
	if len(expired) == 0 {
		return 0, nil
	}
	err = l.vault.Update(ctx, func(tx *vault.Txn) error {
		for _, row := range expired {
			if err := l.deleteExpiredImpression(tx, row); err != nil {
				return err
			}
			removed++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("prune expired personal impressions: %w", err)
	}

	return removed, nil
}

func (l *Learning) deleteExpiredImpression(
	tx *vault.Txn,
	row expiredPersonalImpression,
) error {
	key := vault.Key(row.hash)
	impression, found, err := l.impressions.Get(tx, key)
	if err != nil {
		return wrapPersonalError("read expired impression", err)
	}
	if found {
		return l.deleteImpression(tx, key, impression)
	}
	if _, err := l.impressionExpiry.Delete(tx, vault.Key(row.key)); err != nil {
		return wrapPersonalError("delete stale impression expiry", err)
	}

	return nil
}

func (l *Learning) deleteImpression(
	tx *vault.Txn,
	key vault.Key,
	impression personalImpression,
) error {
	if _, err := l.impressions.Delete(tx, key); err != nil {
		return wrapPersonalError("delete impression", err)
	}
	if _, err := l.impressionExpiry.Delete(
		tx,
		vault.Key(personalExpiryKeyTime(impression.ExpiresAt, string(key))),
	); err != nil {
		return wrapPersonalError("delete impression expiry", err)
	}
	count, found, err := l.impressionCounts.Get(tx, vault.Key(impression.AccountID))
	if err != nil || !found {
		return wrapPersonalError("read impression count for delete", err)
	}
	if count <= 1 {
		_, err = l.impressionCounts.Delete(tx, vault.Key(impression.AccountID))
	} else {
		err = l.impressionCounts.Put(tx, vault.Key(impression.AccountID), count-1)
	}
	return wrapPersonalError("update impression count after delete", err)
}

func (l *Learning) RunRetention(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		_, _ = l.PruneExpired(ctx, 500)
		_, _ = l.PruneExpiredImpressions(ctx, 500)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (l *Learning) recordEvent(ctx context.Context, event personalEvent) error {
	if event.ID == "" {
		event.ID = randomIdentifier()
	}
	if event.ID == "" {
		return errors.New("generate personal event identity")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = l.now()
	}
	if err := l.vault.Update(
		ctx,
		func(tx *vault.Txn) error { return l.putEvent(tx, event) },
	); err != nil {
		return fmt.Errorf("save personal search event: %w", err)
	}
	return nil
}

func (l *Learning) putEvent(tx *vault.Txn, event personalEvent) error {
	key := personalEventKey(event)
	count, found, err := l.eventCounts.Get(tx, vault.Key(event.AccountID))
	if err != nil {
		return wrapPersonalError("read account event count", err)
	}
	if found && count >= maximumPersonalEvents {
		var oldest vault.Key
		if err := l.events.Scan(
			tx,
			vault.Key(event.AccountID+"\x00"),
			func(key vault.Key, _ personalEvent) (bool, error) {
				oldest = key
				return false, nil
			},
		); err != nil {
			return wrapPersonalError("find oldest account event", err)
		}
		if len(oldest) > 0 {
			if _, err := l.deleteEvent(tx, oldest); err != nil {
				return wrapPersonalError("remove oldest account event", err)
			}
			count--
		}
	}
	if err := l.events.Put(tx, vault.Key(key), event); err != nil {
		return wrapPersonalError("store personal search event", err)
	}
	if err := l.eventExpiry.Put(tx, vault.Key(personalEventExpiryKey(event)), key); err != nil {
		return wrapPersonalError("index personal event expiry", err)
	}
	if err := l.eventCounts.Put(tx, vault.Key(event.AccountID), count+1); err != nil {
		return wrapPersonalError("increment account event count", err)
	}

	return nil
}

func (l *Learning) deleteEvent(tx *vault.Txn, key vault.Key) (bool, error) {
	event, found, err := l.events.Get(tx, key)
	if err != nil || !found {
		return false, wrapPersonalError("read personal search event", err)
	}
	if _, err := l.events.Delete(tx, key); err != nil {
		return false, wrapPersonalError("delete personal search event", err)
	}
	if _, err := l.eventExpiry.Delete(tx, vault.Key(personalEventExpiryKey(event))); err != nil {
		return false, wrapPersonalError("delete personal event expiry", err)
	}
	count, found, err := l.eventCounts.Get(tx, vault.Key(event.AccountID))
	if err != nil {
		return false, wrapPersonalError("read account event count for delete", err)
	}
	if found {
		if count <= 1 {
			_, err = l.eventCounts.Delete(tx, vault.Key(event.AccountID))
		} else {
			err = l.eventCounts.Put(tx, vault.Key(event.AccountID), count-1)
		}
	}
	return true, wrapPersonalError("update account event count after delete", err)
}

func personalEventKey(event personalEvent) string {
	return event.AccountID + "\x00" + fmt.Sprintf(
		"%020d",
		event.CreatedAt.UnixNano(),
	) + "\x00" + event.ID
}

func personalEventExpiryKey(event personalEvent) string {
	return fmt.Sprintf("%020d", event.CreatedAt.UnixNano()) + "\x00" + personalEventKey(event)
}

func personalExpiryKeyTime(timestamp time.Time, identity string) string {
	return fmt.Sprintf("%020d", timestamp.UnixNano()) + "\x00" + identity
}

func timestampFromExpiryKey(key vault.Key) (int64, bool) {
	text, _, found := strings.Cut(string(key), "\x00")
	if !found || len(text) != 20 {
		return 0, false
	}
	var timestamp int64
	if _, err := fmt.Sscanf(text, "%d", &timestamp); err != nil {
		return 0, false
	}
	return timestamp, true
}

func decayWeight(halfLives float64) float64 {
	return math.Exp2(-halfLives)
}

func textTerms(value string) []string {
	terms := make([]string, 0, 12)
	for _, part := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if len([]rune(part)) < 2 {
			continue
		}
		terms = append(terms, part)
		if len(terms) >= 24 {
			break
		}
	}
	return terms
}

func resultHost(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
}

func resultTerms(result searchcore.Result) []string {
	return textTerms(result.Title + " " + result.Snippet + " " + result.URL)
}

func personalizedSignal(profile InterestProfile, result searchcore.Result) (float64, string) {
	termSignal := 0.0
	termCount := 0
	for _, term := range resultTerms(result) {
		if weight := profile.Terms[term]; weight > 0 {
			termSignal += weight
			termCount++
		}
	}
	if termCount > 0 {
		termSignal /= float64(termCount)
	}
	host := result.Host
	if host == "" {
		host = resultHost(result.URL)
	}
	hostSignal := profile.Hosts[strings.ToLower(strings.TrimSuffix(host, "."))]
	for preferred, weight := range profile.Hosts {
		if host != preferred && strings.HasSuffix(strings.ToLower(host), "."+preferred) &&
			weight > hostSignal {
			hostSignal = weight
		}
	}
	signal := min(1.0, termSignal*0.65+hostSignal*0.75)
	if signal <= 0 {
		return 0, ""
	}
	if hostSignal > 0 && hostSignal >= termSignal {
		return signal, "You have previously clicked a result from this site."
	}
	return signal, "This result matches topics from your recent searches."
}

func rerankForAccount(response searchcore.Response, profile InterestProfile) searchcore.Response {
	if len(response.Results) < 2 || len(profile.Terms) == 0 && len(profile.Hosts) == 0 {
		return response
	}
	type rankedResult struct {
		result searchcore.Result
		score  float64
	}
	ranked := make([]rankedResult, len(response.Results))
	for index, result := range response.Results {
		signal, reason := personalizedSignal(profile, result)
		boost := personalHistoryBoost * signal
		result.Score += boost
		if reason != "" {
			result.PersonalizedReason = reason
		}
		ranked[index] = rankedResult{result: result, score: 1/float64(60+index+1) + boost}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	response.Results = make([]searchcore.Result, len(ranked))
	for index, row := range ranked {
		response.Results[index] = row.result
	}

	return response
}
