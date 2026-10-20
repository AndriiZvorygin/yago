// Package searchdemand stores privacy-bounded search interest and divides a
// measured crawl budget among topics using a rolling time-decayed score.
package searchdemand

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/D4rk4/yago/yagonode/internal/vault"
)

const (
	topicBucket       vault.Name = "search_demand_topics"
	clientBucket      vault.Name = "search_demand_clients"
	secretBucket      vault.Name = "search_demand_secret"
	runBucket         vault.Name = "search_demand_runs"
	reservationBucket vault.Name = "search_demand_reservations"
	secretKey                    = "hmac"
)

type Config struct {
	Enabled                   bool
	DemandShare               float64
	MaximumTopicShare         float64
	DecayHalfLife             time.Duration
	Retention                 time.Duration
	ProbePageLimit            uint64
	MaximumTopics             uint32
	MaximumClientDays         uint32
	AnonymousDailyCredits     uint32
	AuthenticatedDailyCredits uint32
	PerTopicDailyCredits      uint32
}

type RecordSearchInput struct {
	Query         string
	ClientAddress string
	Authenticated bool
	At            time.Time
	Config        Config
}

type RunSnapshotInput struct {
	RunID         string
	ReservationID string
	Attempts      uint64
	Indexed       uint64
	Terminal      bool
	At            time.Time
}

type topicProgressDelta struct {
	topicID       string
	reservationID string
	day           string
	attempts      uint64
	indexed       uint64
}

func DefaultConfig() Config {
	return Config{
		DemandShare:               0.5,
		MaximumTopicShare:         0.5,
		DecayHalfLife:             7 * 24 * time.Hour,
		Retention:                 28 * 24 * time.Hour,
		ProbePageLimit:            25,
		MaximumTopics:             20_000,
		MaximumClientDays:         100_000,
		AnonymousDailyCredits:     20,
		AuthenticatedDailyCredits: 100,
		PerTopicDailyCredits:      3,
	}
}

type Event struct {
	At    time.Time `json:"at"`
	Count uint32    `json:"count"`
}

type Topic struct {
	ID                  string            `json:"id"`
	Query               string            `json:"query"`
	Events              []Event           `json:"events"`
	FirstSeen           time.Time         `json:"first_seen"`
	LastSeen            time.Time         `json:"last_seen"`
	LastScheduledAt     time.Time         `json:"last_scheduled_at"`
	UsefulDocuments     uint64            `json:"useful_documents"`
	ProbeAttempts       uint64            `json:"probe_attempts"`
	DailyAttempts       map[string]uint64 `json:"daily_attempts"`
	DailyReserved       map[string]uint64 `json:"daily_reserved"`
	DailyReservationIDs map[string]string `json:"daily_reservation_ids"`
}

type clientDay struct {
	Total   uint32            `json:"total"`
	ByTopic map[string]uint32 `json:"by_topic"`
}

type dailyTally struct {
	Attempts uint64 `json:"attempts"`
}

type runProgress struct {
	TopicID       string    `json:"topic_id"`
	ReservationID string    `json:"reservation_id"`
	Attempts      uint64    `json:"attempts"`
	Indexed       uint64    `json:"indexed"`
	UpdatedAt     time.Time `json:"updated_at"`
	Terminal      bool      `json:"terminal"`
}

type reservation struct {
	TopicID string    `json:"topic_id"`
	Day     string    `json:"day"`
	Pages   uint64    `json:"pages"`
	Created time.Time `json:"created"`
}

type ReservationState uint8

const (
	ReservationBusy ReservationState = iota
	ReservationCreated
	ReservationResumed
)

type (
	topicCodec       struct{}
	clientCodec      struct{}
	secretCodec      struct{}
	dailyCodec       struct{}
	runCodec         struct{}
	reservationCodec struct{}
)

func (topicCodec) Encode(value Topic) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal search-demand topic: %w", err)
	}
	return raw, nil
}

func (topicCodec) Decode(raw []byte) (Topic, error) {
	var value Topic
	if err := json.Unmarshal(raw, &value); err != nil {
		return Topic{}, fmt.Errorf("unmarshal search-demand topic: %w", err)
	}
	return value, nil
}

func (clientCodec) Encode(value clientDay) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal search-demand client quota: %w", err)
	}
	return raw, nil
}

func (clientCodec) Decode(raw []byte) (clientDay, error) {
	var value clientDay
	if err := json.Unmarshal(raw, &value); err != nil {
		return clientDay{}, fmt.Errorf("unmarshal search-demand client quota: %w", err)
	}
	return value, nil
}

func (secretCodec) Encode(
	value []byte,
) ([]byte, error) {
	return append([]byte(nil), value...), nil
}

func (secretCodec) Decode(
	raw []byte,
) ([]byte, error) {
	return append([]byte(nil), raw...), nil
}

func (dailyCodec) Encode(value dailyTally) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal search-demand daily tally: %w", err)
	}
	return raw, nil
}

func (dailyCodec) Decode(raw []byte) (dailyTally, error) {
	var value dailyTally
	if err := json.Unmarshal(raw, &value); err != nil {
		return dailyTally{}, fmt.Errorf("unmarshal search-demand daily tally: %w", err)
	}
	return value, nil
}

func (runCodec) Encode(value runProgress) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal search-demand run snapshot: %w", err)
	}
	return raw, nil
}

func (runCodec) Decode(raw []byte) (runProgress, error) {
	var value runProgress
	if err := json.Unmarshal(raw, &value); err != nil {
		return runProgress{}, fmt.Errorf("unmarshal search-demand run snapshot: %w", err)
	}
	return value, nil
}

func (reservationCodec) Encode(value reservation) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal search-demand reservation: %w", err)
	}
	return raw, nil
}

func (reservationCodec) Decode(raw []byte) (reservation, error) {
	var value reservation
	if err := json.Unmarshal(raw, &value); err != nil {
		return reservation{}, fmt.Errorf("unmarshal search-demand reservation: %w", err)
	}
	return value, nil
}

type Store struct {
	vault        *vault.Vault
	topics       *vault.Collection[Topic]
	clients      *vault.Collection[clientDay]
	secrets      *vault.Collection[[]byte]
	daily        *vault.Collection[dailyTally]
	runs         *vault.Collection[runProgress]
	reservations *vault.Collection[reservation]
	secret       []byte
}

func Open(v *vault.Vault) (*Store, error) {
	if v == nil {
		return nil, fmt.Errorf("search demand storage is unavailable")
	}
	topics, err := vault.Register(v, topicBucket, topicCodec{})
	if err != nil {
		return nil, fmt.Errorf("register search demand topics: %w", err)
	}
	clients, err := vault.Register(v, clientBucket, clientCodec{})
	if err != nil {
		return nil, fmt.Errorf("register search demand client quotas: %w", err)
	}
	secrets, err := vault.Register(v, secretBucket, secretCodec{})
	if err != nil {
		return nil, fmt.Errorf("register search demand key: %w", err)
	}
	daily, err := vault.Register(v, "search_demand_daily", dailyCodec{})
	if err != nil {
		return nil, fmt.Errorf("register search demand daily tally: %w", err)
	}
	runs, err := vault.Register(v, runBucket, runCodec{})
	if err != nil {
		return nil, fmt.Errorf("register search demand run progress: %w", err)
	}
	reservations, err := vault.Register(v, reservationBucket, reservationCodec{})
	if err != nil {
		return nil, fmt.Errorf("register search demand reservations: %w", err)
	}
	store := &Store{
		vault: v, topics: topics, clients: clients, secrets: secrets,
		daily: daily, runs: runs, reservations: reservations,
	}
	if err := store.ensureSecret(context.Background()); err != nil {
		return nil, err
	}

	return store, nil
}

func (s *Store) ensureSecret(ctx context.Context) error {
	var secret []byte
	if err := s.vault.View(ctx, func(tx *vault.Txn) error {
		value, found, err := s.secrets.Get(tx, vault.Key(secretKey))
		if err != nil {
			return fmt.Errorf("read search-demand key: %w", err)
		}
		if found {
			secret = value
		}
		return nil
	}); err != nil {
		return fmt.Errorf("read search demand key: %w", err)
	}
	if len(secret) == 0 {
		candidate := make([]byte, 32)
		if _, err := rand.Read(candidate); err != nil {
			return fmt.Errorf("generate search demand key: %w", err)
		}
		if err := s.vault.Update(ctx, func(tx *vault.Txn) error {
			value, found, err := s.secrets.Get(tx, vault.Key(secretKey))
			if err != nil {
				return fmt.Errorf("check search-demand key: %w", err)
			}
			if found {
				secret = value
				return nil
			}
			if err := s.secrets.Put(tx, vault.Key(secretKey), candidate); err != nil {
				return fmt.Errorf("save search-demand key: %w", err)
			}
			secret = candidate
			return nil
		}); err != nil {
			return fmt.Errorf("persist search demand key: %w", err)
		}
	}
	if len(secret) != 32 {
		return fmt.Errorf("stored search demand key has invalid length %d", len(secret))
	}
	s.secret = secret

	return nil
}

func NormalizeQuery(raw string) string {
	if !utf8.ValidString(raw) {
		return ""
	}
	query := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(raw))), " ")
	if query == "" || utf8.RuneCountInString(query) > 256 {
		return ""
	}
	return query
}

func (s *Store) topicID(query string) string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte("topic\x00" + query))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Store) clientID(day, address string) string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(day + "\x00" + address))
	return day + ":" + hex.EncodeToString(mac.Sum(nil))
}

func (s *Store) RecordSearch(
	ctx context.Context,
	input RecordSearchInput,
) (Topic, bool, error) {
	if !input.Config.Enabled {
		return Topic{}, false, nil
	}
	query := NormalizeQuery(input.Query)
	clientAddress := strings.TrimSpace(input.ClientAddress)
	if query == "" || clientAddress == "" {
		return Topic{}, false, nil
	}
	if input.Config.PerTopicDailyCredits == 0 || input.Config.MaximumTopics == 0 ||
		input.Config.MaximumClientDays == 0 || input.Config.Retention <= 0 {
		return Topic{}, false, fmt.Errorf("invalid search demand quota configuration")
	}
	now := input.At.UTC()
	day := now.Format("2006-01-02")
	input.Query = query
	input.ClientAddress = clientAddress
	input.At = now
	topicID := s.topicID(query)
	clientKey := s.clientID(day, clientAddress)
	var topic Topic
	accepted := false
	err := s.vault.Update(ctx, func(tx *vault.Txn) error {
		var err error
		topic, accepted, err = s.recordSearchTransaction(tx, topicID, clientKey, input)
		return err
	})
	if err != nil {
		return Topic{}, false, fmt.Errorf("record search demand: %w", err)
	}
	if !accepted {
		return Topic{}, false, nil
	}
	return topic, true, nil
}

func (s *Store) recordSearchTransaction(
	tx *vault.Txn,
	topicID string,
	clientKey string,
	input RecordSearchInput,
) (Topic, bool, error) {
	quota, eligible, err := s.searchClientQuota(
		tx, topicID, clientKey, input.Authenticated, input.Config,
	)
	if err != nil || !eligible {
		return Topic{}, false, err
	}
	topic, eligible, err := s.searchTopic(tx, topicID, input.Query, input.At, input.Config)
	if err != nil || !eligible {
		return Topic{}, false, err
	}
	quota.Total++
	quota.ByTopic[topicID]++
	if err := s.clients.Put(tx, vault.Key(clientKey), quota); err != nil {
		return Topic{}, false, fmt.Errorf("save search-demand client quota: %w", err)
	}
	topic = recordTopicSearch(topic, input.At, input.Config.Retention)
	if err := s.topics.Put(tx, vault.Key(topicID), topic); err != nil {
		return Topic{}, false, fmt.Errorf("save search-demand topic: %w", err)
	}
	return topic, true, nil
}

func (s *Store) searchClientQuota(
	tx *vault.Txn,
	topicID string,
	clientKey string,
	authenticated bool,
	cfg Config,
) (clientDay, bool, error) {
	quota, found, err := s.clients.Get(tx, vault.Key(clientKey))
	if err != nil {
		return clientDay{}, false, fmt.Errorf("read search-demand client quota: %w", err)
	}
	if !found {
		count, err := s.clients.Len(tx)
		if err != nil {
			return clientDay{}, false, fmt.Errorf("count search-demand client quotas: %w", err)
		}
		if count >= int(cfg.MaximumClientDays) {
			return clientDay{}, false, nil
		}
		quota.ByTopic = make(map[string]uint32)
	}
	limit := cfg.AnonymousDailyCredits
	if authenticated {
		limit = cfg.AuthenticatedDailyCredits
	}
	eligible := quota.Total < limit && quota.ByTopic[topicID] < cfg.PerTopicDailyCredits
	return quota, eligible, nil
}

func (s *Store) searchTopic(
	tx *vault.Txn,
	topicID string,
	query string,
	now time.Time,
	cfg Config,
) (Topic, bool, error) {
	topic, found, err := s.topics.Get(tx, vault.Key(topicID))
	if err != nil {
		return Topic{}, false, fmt.Errorf("read search-demand topic: %w", err)
	}
	if found {
		return topic, true, nil
	}
	count, err := s.topics.Len(tx)
	if err != nil {
		return Topic{}, false, fmt.Errorf("count search-demand topics: %w", err)
	}
	if count >= int(cfg.MaximumTopics) {
		return Topic{}, false, nil
	}
	return Topic{ID: topicID, Query: query, FirstSeen: now}, true, nil
}

func recordTopicSearch(topic Topic, now time.Time, retention time.Duration) Topic {
	cutoff := now.Add(-retention)
	retained := topic.Events[:0]
	for _, event := range topic.Events {
		if !event.At.Before(cutoff) {
			retained = append(retained, event)
		}
	}
	topic.Events = retained
	bucketAt := now.Truncate(24 * time.Hour).Add(12 * time.Hour)
	for index := range topic.Events {
		if topic.Events[index].At.Equal(bucketAt) {
			if topic.Events[index].Count < math.MaxUint32 {
				topic.Events[index].Count++
			}
			topic.LastSeen = now
			return topic
		}
	}
	topic.Events = append(topic.Events, Event{At: bucketAt, Count: 1})
	topic.LastSeen = now
	return topic
}

func (s *Store) Topic(ctx context.Context, id string) (Topic, error) {
	var topic Topic
	err := s.vault.View(ctx, func(tx *vault.Txn) error {
		value, found, err := s.topics.Get(tx, vault.Key(id))
		if err != nil {
			return fmt.Errorf("read search-demand topic: %w", err)
		}
		if found {
			topic = value
		}
		return nil
	})
	if err != nil {
		return Topic{}, fmt.Errorf("read search demand topic: %w", err)
	}
	return topic, nil
}

func (s *Store) Topics(
	ctx context.Context,
	now time.Time,
	retention time.Duration,
) ([]Topic, error) {
	if retention <= 0 {
		return nil, fmt.Errorf("search demand retention must be positive")
	}
	var topics []Topic
	cutoff := now.UTC().Add(-retention)
	err := s.vault.View(ctx, func(tx *vault.Txn) error {
		if err := s.topics.Scan(tx, nil, func(_ vault.Key, topic Topic) (bool, error) {
			events := topic.Events[:0]
			for _, event := range topic.Events {
				if !event.At.Before(cutoff) && !event.At.After(now.UTC().Add(12*time.Hour)) {
					events = append(events, event)
				}
			}
			topic.Events = events
			if len(events) > 0 {
				topics = append(topics, topic)
			}
			return true, nil
		}); err != nil {
			return fmt.Errorf("scan search-demand topics: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan search demand topics: %w", err)
	}
	return topics, nil
}

func (s *Store) MarkScheduled(ctx context.Context, topicID string, now time.Time) error {
	err := s.vault.Update(ctx, func(tx *vault.Txn) error {
		topic, found, err := s.topics.Get(tx, vault.Key(topicID))
		if err != nil {
			return fmt.Errorf("read search-demand topic before scheduling: %w", err)
		}
		if !found {
			return nil
		}
		topic.LastScheduledAt = now.UTC()
		if err := s.topics.Put(tx, vault.Key(topicID), topic); err != nil {
			return fmt.Errorf("save search-demand schedule: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("mark search-demand topic scheduled: %w", err)
	}
	return nil
}

func (s *Store) Reserve(
	ctx context.Context,
	topicID string,
	day string,
	runID string,
	pages uint64,
) (ReservationState, error) {
	if topicID == "" || day == "" || runID == "" || pages == 0 {
		return ReservationBusy, fmt.Errorf(
			"topic, day, run id and positive page budget are required",
		)
	}
	state := ReservationBusy
	err := s.vault.Update(ctx, func(tx *vault.Txn) error {
		var err error
		state, err = s.reserveTransaction(tx, topicID, day, runID, pages)
		return err
	})
	if err != nil {
		return ReservationBusy, fmt.Errorf("reserve topic crawl budget: %w", err)
	}
	return state, nil
}

func (s *Store) reserveTransaction(
	tx *vault.Txn,
	topicID string,
	day string,
	runID string,
	pages uint64,
) (ReservationState, error) {
	existing, found, err := s.reservations.Get(tx, vault.Key(runID))
	if err != nil {
		return ReservationBusy, fmt.Errorf("read crawl reservation: %w", err)
	}
	if found {
		if existing.TopicID != topicID || existing.Day != day {
			return ReservationBusy, fmt.Errorf(
				"reservation identity conflicts with another topic or day",
			)
		}
		return ReservationResumed, nil
	}
	topic, found, err := s.topics.Get(tx, vault.Key(topicID))
	if err != nil {
		return ReservationBusy, fmt.Errorf("read topic before reserving crawl budget: %w", err)
	}
	if !found {
		return ReservationBusy, nil
	}
	if topic.DailyReserved == nil {
		topic.DailyReserved = make(map[string]uint64)
	}
	if topic.DailyReservationIDs == nil {
		topic.DailyReservationIDs = make(map[string]string)
	}
	if active := topic.DailyReservationIDs[day]; active != "" && active != runID {
		return ReservationBusy, nil
	}
	topic.DailyReserved[day] += pages
	topic.DailyReservationIDs[day] = runID
	if err := s.topics.Put(tx, vault.Key(topicID), topic); err != nil {
		return ReservationBusy, fmt.Errorf("save topic crawl reservation: %w", err)
	}
	if err := s.reservations.Put(tx, vault.Key(runID), reservation{
		TopicID: topicID, Day: day, Pages: pages, Created: time.Now().UTC(),
	}); err != nil {
		return ReservationBusy, fmt.Errorf("save crawl reservation record: %w", err)
	}
	return ReservationCreated, nil
}

func (s *Store) ReleaseReservation(ctx context.Context, runID string) error {
	err := s.vault.Update(ctx, func(tx *vault.Txn) error {
		return s.releaseReservationTransaction(tx, runID)
	})
	if err != nil {
		return fmt.Errorf("release crawl reservation: %w", err)
	}
	return nil
}

func (s *Store) releaseReservationTransaction(tx *vault.Txn, runID string) error {
	reserved, found, err := s.reservations.Get(tx, vault.Key(runID))
	if err != nil {
		return fmt.Errorf("read crawl reservation to release: %w", err)
	}
	if !found {
		return nil
	}
	if err := s.releaseTopicReservation(tx, runID, reserved); err != nil {
		return err
	}
	if _, err := s.reservations.Delete(tx, vault.Key(runID)); err != nil {
		return fmt.Errorf("delete released crawl reservation: %w", err)
	}
	return nil
}

func (s *Store) releaseTopicReservation(
	tx *vault.Txn,
	runID string,
	reserved reservation,
) error {
	topic, exists, err := s.topics.Get(tx, vault.Key(reserved.TopicID))
	if err != nil {
		return fmt.Errorf("read topic to release crawl reservation: %w", err)
	}
	if !exists {
		return nil
	}
	if reserved.Pages > 0 && topic.DailyReserved[reserved.Day] >= reserved.Pages {
		topic.DailyReserved[reserved.Day] -= reserved.Pages
		if topic.DailyReserved[reserved.Day] == 0 {
			delete(topic.DailyReserved, reserved.Day)
		}
	}
	if topic.DailyReservationIDs[reserved.Day] == runID {
		delete(topic.DailyReservationIDs, reserved.Day)
	}
	if err := s.topics.Put(tx, vault.Key(reserved.TopicID), topic); err != nil {
		return fmt.Errorf("save released topic crawl budget: %w", err)
	}
	return nil
}

func (s *Store) RecordRunSnapshot(ctx context.Context, snapshot RunSnapshotInput) error {
	if snapshot.RunID == "" {
		return nil
	}
	snapshot.At = snapshot.At.UTC()
	err := s.vault.Update(ctx, func(tx *vault.Txn) error {
		return s.recordRunSnapshotTransaction(tx, snapshot)
	})
	if err != nil {
		return fmt.Errorf("record crawl run snapshot: %w", err)
	}
	return nil
}

func (s *Store) recordRunSnapshotTransaction(
	tx *vault.Txn,
	snapshot RunSnapshotInput,
) error {
	run, found, err := s.loadRunProgress(tx, snapshot.RunID, snapshot.ReservationID)
	if err != nil || found && run.Terminal {
		return err
	}
	attemptDelta := delta(snapshot.Attempts, run.Attempts)
	indexedDelta := delta(snapshot.Indexed, run.Indexed)
	run.Attempts = max(run.Attempts, snapshot.Attempts)
	run.Indexed = max(run.Indexed, snapshot.Indexed)
	run.ReservationID = snapshot.ReservationID
	day := snapshot.At.Format("2006-01-02")
	if err := s.recordDailyAttempts(tx, day, attemptDelta); err != nil {
		return err
	}
	if err := s.recordTopicProgress(tx, topicProgressDelta{
		topicID: run.TopicID, reservationID: snapshot.ReservationID,
		day: day, attempts: attemptDelta, indexed: indexedDelta,
	}); err != nil {
		return err
	}
	if snapshot.Terminal {
		if err := s.completeRunReservation(tx, snapshot.ReservationID); err != nil {
			return err
		}
		run.Terminal = true
	}
	run.UpdatedAt = snapshot.At
	if err := s.runs.Put(tx, vault.Key(snapshot.RunID), run); err != nil {
		return fmt.Errorf("save crawl run snapshot: %w", err)
	}
	return nil
}

func delta(current uint64, previous uint64) uint64 {
	if current <= previous {
		return 0
	}
	return current - previous
}

func (s *Store) loadRunProgress(
	tx *vault.Txn,
	runID string,
	reservationID string,
) (runProgress, bool, error) {
	run, found, err := s.runs.Get(tx, vault.Key(runID))
	if err != nil {
		return runProgress{}, false, fmt.Errorf("read crawl run snapshot: %w", err)
	}
	if found {
		return run, true, nil
	}
	run = runProgress{ReservationID: reservationID}
	if reservationID == "" {
		return run, false, nil
	}
	reserved, exists, err := s.reservations.Get(tx, vault.Key(reservationID))
	if err != nil {
		return runProgress{}, false, fmt.Errorf("read run crawl reservation: %w", err)
	}
	if exists {
		run.TopicID = reserved.TopicID
	}
	return run, false, nil
}

func (s *Store) recordDailyAttempts(tx *vault.Txn, day string, amount uint64) error {
	if amount == 0 {
		return nil
	}
	total, found, err := s.daily.Get(tx, vault.Key(day))
	if err != nil {
		return fmt.Errorf("read daily crawl-attempt tally: %w", err)
	}
	if !found {
		total = dailyTally{}
	}
	total.Attempts += amount
	if err := s.daily.Put(tx, vault.Key(day), total); err != nil {
		return fmt.Errorf("save daily crawl-attempt tally: %w", err)
	}
	return nil
}

func (s *Store) recordTopicProgress(tx *vault.Txn, progress topicProgressDelta) error {
	if progress.topicID == "" || progress.attempts == 0 && progress.indexed == 0 {
		return nil
	}
	topic, found, err := s.topics.Get(tx, vault.Key(progress.topicID))
	if err != nil {
		return fmt.Errorf("read focused crawl topic progress: %w", err)
	}
	if !found {
		return nil
	}
	if topic.DailyAttempts == nil {
		topic.DailyAttempts = make(map[string]uint64)
	}
	topic.DailyAttempts[progress.day] += progress.attempts
	topic.ProbeAttempts += progress.attempts
	topic.UsefulDocuments += progress.indexed
	if err := s.consumeReservation(
		tx,
		topic,
		progress.reservationID,
		progress.attempts,
	); err != nil {
		return err
	}
	if err := s.topics.Put(tx, vault.Key(progress.topicID), topic); err != nil {
		return fmt.Errorf("save focused crawl topic progress: %w", err)
	}
	return nil
}

func (s *Store) consumeReservation(
	tx *vault.Txn,
	topic Topic,
	reservationID string,
	attempts uint64,
) error {
	if reservationID == "" || attempts == 0 {
		return nil
	}
	reserved, found, err := s.reservations.Get(tx, vault.Key(reservationID))
	if err != nil {
		return fmt.Errorf("read focused crawl reservation progress: %w", err)
	}
	if !found || reserved.Pages == 0 {
		return nil
	}
	consumed := min(attempts, reserved.Pages)
	reserved.Pages -= consumed
	if topic.DailyReserved[reserved.Day] >= consumed {
		topic.DailyReserved[reserved.Day] -= consumed
		if topic.DailyReserved[reserved.Day] == 0 {
			delete(topic.DailyReserved, reserved.Day)
		}
	}
	if err := s.reservations.Put(tx, vault.Key(reservationID), reserved); err != nil {
		return fmt.Errorf("save focused crawl reservation progress: %w", err)
	}
	return nil
}

func (s *Store) completeRunReservation(tx *vault.Txn, reservationID string) error {
	if reservationID == "" {
		return nil
	}
	reserved, found, err := s.reservations.Get(tx, vault.Key(reservationID))
	if err != nil {
		return fmt.Errorf("read completed crawl reservation: %w", err)
	}
	if !found {
		return nil
	}
	topic, topicFound, err := s.topics.Get(tx, vault.Key(reserved.TopicID))
	if err != nil {
		return fmt.Errorf("read topic for completed crawl reservation: %w", err)
	}
	if topicFound {
		if topic.DailyReserved[reserved.Day] >= reserved.Pages {
			topic.DailyReserved[reserved.Day] -= reserved.Pages
			if topic.DailyReserved[reserved.Day] == 0 {
				delete(topic.DailyReserved, reserved.Day)
			}
		}
		if topic.DailyReservationIDs[reserved.Day] == reservationID {
			delete(topic.DailyReservationIDs, reserved.Day)
		}
		if err := s.topics.Put(tx, vault.Key(reserved.TopicID), topic); err != nil {
			return fmt.Errorf("save completed topic crawl reservation: %w", err)
		}
	}
	if _, err := s.reservations.Delete(tx, vault.Key(reservationID)); err != nil {
		return fmt.Errorf("delete completed crawl reservation: %w", err)
	}
	return nil
}

func (s *Store) MeasuredDailyCapacity(ctx context.Context, now time.Time) (uint64, error) {
	start := now.UTC().AddDate(0, 0, -7).Format("2006-01-02")
	end := now.UTC().AddDate(0, 0, -1).Format("2006-01-02")
	var attempts uint64
	var days uint64
	err := s.vault.View(ctx, func(tx *vault.Txn) error {
		if err := s.daily.Scan(tx, nil, func(key vault.Key, tally dailyTally) (bool, error) {
			day := string(key)
			if day >= start && day <= end && tally.Attempts > 0 {
				attempts += tally.Attempts
				days++
			}
			return true, nil
		}); err != nil {
			return fmt.Errorf("scan daily crawl-attempt tallies: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("measure search demand capacity: %w", err)
	}
	if days == 0 {
		return 0, nil
	}
	return attempts / days, nil
}

func TopicWeight(topic Topic, now time.Time, halfLife time.Duration) float64 {
	if halfLife <= 0 {
		return 0
	}
	weight := 0.0
	for _, event := range topic.Events {
		age := now.Sub(event.At)
		if age < 0 {
			age = 0
		}
		count := event.Count
		if count == 0 {
			count = 1
		}
		weight += float64(count) * math.Exp2(-float64(age)/float64(halfLife))
	}
	return weight
}

type Budget struct {
	TopicID   string
	Query     string
	Pages     uint64
	Share     uint64
	ProbeOnly bool
	Reason    string
}

func Allocate(
	topics []Topic,
	capacity uint64,
	reservedOrdinary uint64,
	now time.Time,
	cfg Config,
) []Budget {
	pool, eligible := demandPool(capacity, reservedOrdinary, cfg)
	if !eligible || len(topics) == 0 {
		return nil
	}
	day := now.UTC().Format("2006-01-02")
	candidates, spent := allocationCandidates(topics, now, cfg, pool, day)
	if spent >= pool {
		return nil
	}
	distributeDemand(candidates, pool-spent)
	return demandBudgets(candidates, day)
}

type allocationCandidate struct {
	topic     Topic
	weight    float64
	room      uint64
	allocated uint64
	fraction  float64
}

func demandPool(capacity, reservedOrdinary uint64, cfg Config) (uint64, bool) {
	if cfg.DemandShare <= 0 || cfg.DemandShare > 1 ||
		cfg.MaximumTopicShare <= 0 || cfg.MaximumTopicShare > 1 || cfg.DecayHalfLife <= 0 ||
		reservedOrdinary >= capacity {
		return 0, false
	}
	pool := uint64(float64(capacity) * cfg.DemandShare)
	return min(pool, capacity-reservedOrdinary), true
}

func allocationCandidates(
	topics []Topic,
	now time.Time,
	cfg Config,
	pool uint64,
	day string,
) ([]allocationCandidate, uint64) {
	maximum := uint64(float64(pool) * cfg.MaximumTopicShare)
	candidates := make([]allocationCandidate, len(topics))
	var spent uint64
	for index, topic := range topics {
		used := topic.DailyAttempts[day] + topic.DailyReserved[day]
		spent += used
		room := uint64(0)
		if used < maximum {
			room = maximum - used
		}
		if topic.UsefulDocuments == 0 {
			room = min(room, probeRoom(topic, cfg.ProbePageLimit))
		}
		candidates[index] = allocationCandidate{
			topic: topic, weight: TopicWeight(topic, now, cfg.DecayHalfLife), room: room,
		}
	}
	return candidates, spent
}

func probeRoom(topic Topic, limit uint64) uint64 {
	reserved := uint64(0)
	for _, pages := range topic.DailyReserved {
		reserved += pages
	}
	used := topic.ProbeAttempts + reserved
	if used >= limit {
		return 0
	}
	return limit - used
}

func distributeDemand(candidates []allocationCandidate, remaining uint64) {
	for remaining > 0 {
		weight := activeDemandWeight(candidates)
		if weight == 0 {
			return
		}
		if capSpend := fillCappedDemand(candidates, remaining, weight); capSpend > 0 {
			remaining -= capSpend
			continue
		}
		remaining -= fillWeightedFloors(candidates, remaining, weight)
		if remaining > 0 {
			assignLargestRemainders(candidates, remaining)
		}
		return
	}
}

func activeDemandWeight(candidates []allocationCandidate) float64 {
	var total float64
	for _, candidate := range candidates {
		if candidate.room > 0 {
			total += candidate.weight
		}
	}
	return total
}

func fillCappedDemand(
	candidates []allocationCandidate,
	available uint64,
	totalWeight float64,
) uint64 {
	var allocated uint64
	for index := range candidates {
		candidate := &candidates[index]
		if candidate.room == 0 {
			continue
		}
		ideal := float64(available) * candidate.weight / totalWeight
		if uint64(math.Floor(ideal)) < candidate.room {
			continue
		}
		candidate.allocated += candidate.room
		allocated += candidate.room
		candidate.room = 0
	}
	return allocated
}

func fillWeightedFloors(
	candidates []allocationCandidate,
	available uint64,
	totalWeight float64,
) uint64 {
	var allocated uint64
	for index := range candidates {
		candidate := &candidates[index]
		if candidate.room == 0 {
			continue
		}
		ideal := float64(available) * candidate.weight / totalWeight
		share := min(uint64(math.Floor(ideal)), candidate.room)
		candidate.allocated += share
		candidate.room -= share
		candidate.fraction = ideal - math.Floor(ideal)
		allocated += share
	}
	return allocated
}

func assignLargestRemainders(candidates []allocationCandidate, remaining uint64) uint64 {
	order := make([]int, 0, len(candidates))
	for index, candidate := range candidates {
		if candidate.room > 0 && candidate.weight > 0 {
			order = append(order, index)
		}
	}
	sort.Slice(order, func(left, right int) bool {
		return higherRemainder(candidates[order[left]], candidates[order[right]])
	})
	var allocated uint64
	for _, index := range order {
		if allocated == remaining {
			break
		}
		candidates[index].allocated++
		candidates[index].room--
		allocated++
	}
	return allocated
}

func higherRemainder(left, right allocationCandidate) bool {
	if left.fraction != right.fraction {
		return left.fraction > right.fraction
	}
	if left.weight != right.weight {
		return left.weight > right.weight
	}
	return left.topic.ID < right.topic.ID
}

func demandBudgets(candidates []allocationCandidate, day string) []Budget {
	budgets := make([]Budget, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.allocated == 0 {
			continue
		}
		probeOnly := candidate.topic.UsefulDocuments == 0
		reason := "decayed search demand share"
		if probeOnly {
			reason = "first-use probe pending useful-document evidence"
		}
		budgets = append(budgets, Budget{
			TopicID:   candidate.topic.ID,
			Query:     candidate.topic.Query,
			Pages:     candidate.allocated,
			Share:     candidate.topic.DailyAttempts[day] + candidate.topic.DailyReserved[day] + candidate.allocated,
			ProbeOnly: probeOnly,
			Reason:    reason,
		})
	}
	sort.Slice(budgets, func(left, right int) bool {
		if budgets[left].Pages != budgets[right].Pages {
			return budgets[left].Pages > budgets[right].Pages
		}
		return budgets[left].TopicID < budgets[right].TopicID
	})
	return budgets
}

func (s *Store) Prune(ctx context.Context, now time.Time, retention time.Duration) error {
	if retention <= 0 {
		return fmt.Errorf("search demand retention must be positive")
	}
	cutoff := now.UTC().Add(-retention)
	plan, err := s.collectPrunePlan(ctx, cutoff)
	if err != nil {
		return err
	}
	if plan.empty() {
		return nil
	}
	if err := s.applyPrunePlan(ctx, plan); err != nil {
		return fmt.Errorf("prune search demand state: %w", err)
	}
	return nil
}

type prunePlan struct {
	staleTopics   []vault.Key
	staleClients  []vault.Key
	staleDays     []vault.Key
	staleRuns     []vault.Key
	trimmedTopics []Topic
}

func (p prunePlan) empty() bool {
	return len(p.staleTopics)+len(p.staleClients)+len(p.staleDays)+len(p.staleRuns)+
		len(p.trimmedTopics) == 0
}

func (s *Store) collectPrunePlan(ctx context.Context, cutoff time.Time) (prunePlan, error) {
	var plan prunePlan
	err := s.vault.View(ctx, func(tx *vault.Txn) error {
		if err := s.collectStaleTopics(tx, cutoff, &plan); err != nil {
			return err
		}
		if err := s.collectStaleClients(tx, cutoff, &plan); err != nil {
			return err
		}
		if err := s.collectStaleDays(tx, cutoff, &plan); err != nil {
			return err
		}
		return s.collectStaleRuns(tx, cutoff, &plan)
	})
	if err != nil {
		return prunePlan{}, fmt.Errorf("scan expired search demand: %w", err)
	}
	return plan, nil
}

func (s *Store) collectStaleTopics(tx *vault.Txn, cutoff time.Time, plan *prunePlan) error {
	err := s.topics.Scan(tx, nil, func(key vault.Key, topic Topic) (bool, error) {
		if topic.LastSeen.Before(cutoff) {
			plan.staleTopics = append(plan.staleTopics, key)
			return true, nil
		}
		if trimTopic(&topic, cutoff) {
			plan.trimmedTopics = append(plan.trimmedTopics, topic)
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("scan search-demand topics: %w", err)
	}
	return nil
}

func trimTopic(topic *Topic, cutoff time.Time) bool {
	changed := false
	retained := topic.Events[:0]
	for _, event := range topic.Events {
		if !event.At.Before(cutoff) {
			retained = append(retained, event)
		}
	}
	if len(retained) != len(topic.Events) {
		topic.Events = retained
		changed = true
	}
	changed = trimDayMap(topic.DailyAttempts, cutoff) || changed
	changed = trimDayMap(topic.DailyReserved, cutoff) || changed
	changed = trimDayMap(topic.DailyReservationIDs, cutoff) || changed
	return changed
}

func trimDayMap[T any](values map[string]T, cutoff time.Time) bool {
	changed := false
	for day := range values {
		if demandDayExpired(day, cutoff) {
			delete(values, day)
			changed = true
		}
	}
	return changed
}

func (s *Store) collectStaleClients(tx *vault.Txn, cutoff time.Time, plan *prunePlan) error {
	err := s.clients.Scan(tx, nil, func(key vault.Key, _ clientDay) (bool, error) {
		day, _, _ := strings.Cut(string(key), ":")
		if demandDayExpired(day, cutoff) {
			plan.staleClients = append(plan.staleClients, key)
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("scan search-demand client quotas: %w", err)
	}
	return nil
}

func (s *Store) collectStaleDays(tx *vault.Txn, cutoff time.Time, plan *prunePlan) error {
	err := s.daily.Scan(tx, nil, func(key vault.Key, _ dailyTally) (bool, error) {
		if demandDayExpired(string(key), cutoff) {
			plan.staleDays = append(plan.staleDays, key)
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("scan search-demand daily tallies: %w", err)
	}
	return nil
}

func (s *Store) collectStaleRuns(tx *vault.Txn, cutoff time.Time, plan *prunePlan) error {
	err := s.runs.Scan(tx, nil, func(key vault.Key, run runProgress) (bool, error) {
		if run.Terminal && run.UpdatedAt.Before(cutoff) {
			plan.staleRuns = append(plan.staleRuns, key)
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("scan search-demand run snapshots: %w", err)
	}
	return nil
}

func (s *Store) applyPrunePlan(ctx context.Context, plan prunePlan) error {
	if err := s.vault.Update(ctx, func(tx *vault.Txn) error {
		return s.applyPrunePlanTransaction(tx, plan)
	}); err != nil {
		return fmt.Errorf("apply search-demand pruning: %w", err)
	}
	return nil
}

func (s *Store) applyPrunePlanTransaction(tx *vault.Txn, plan prunePlan) error {
	for _, key := range plan.staleTopics {
		if _, err := s.topics.Delete(tx, key); err != nil {
			return fmt.Errorf("delete expired topic: %w", err)
		}
	}
	for _, topic := range plan.trimmedTopics {
		if err := s.topics.Put(tx, vault.Key(topic.ID), topic); err != nil {
			return fmt.Errorf("save trimmed topic: %w", err)
		}
	}
	if err := deleteKeys(s.clients, tx, plan.staleClients, "client quota"); err != nil {
		return err
	}
	if err := deleteKeys(s.daily, tx, plan.staleDays, "daily tally"); err != nil {
		return err
	}
	return deleteKeys(s.runs, tx, plan.staleRuns, "run snapshot")
}

func deleteKeys[T any](
	collection *vault.Collection[T],
	tx *vault.Txn,
	keys []vault.Key,
	label string,
) error {
	for _, key := range keys {
		if _, err := collection.Delete(tx, key); err != nil {
			return fmt.Errorf("delete expired %s: %w", label, err)
		}
	}
	return nil
}

func demandDayExpired(day string, cutoff time.Time) bool {
	date, err := time.Parse("2006-01-02", day)
	return err == nil && date.Before(cutoff)
}
