package yagonode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/D4rk4/yago/yagocrawlcontract"
	"github.com/D4rk4/yago/yagomodel"
	"github.com/D4rk4/yago/yagonode/internal/crawldispatch"
	"github.com/D4rk4/yago/yagonode/internal/crawlruns"
	"github.com/D4rk4/yago/yagonode/internal/documentstore"
	"github.com/D4rk4/yago/yagonode/internal/httpguard"
	"github.com/D4rk4/yago/yagonode/internal/searchcore"
	"github.com/D4rk4/yago/yagonode/internal/searchdemand"
	"github.com/D4rk4/yago/yagonode/internal/tavilyapi"
	"github.com/D4rk4/yago/yagoproto"
)

const (
	searchDemandProfilePrefix  = "search-demand:"
	searchDemandRefreshEvery   = 15 * time.Minute
	searchDemandTopicInterval  = 24 * time.Hour
	searchDemandTaskTimeout    = 30 * time.Second
	searchDemandMaxStarts      = 1000
	searchDemandTopicsPerCycle = 100
)

type demandRequestIdentity struct {
	clientAddress string
	authenticated bool
}

type demandRequestIdentityKey struct{}

func demandIdentityFromContext(ctx context.Context) (demandRequestIdentity, bool) {
	identity, ok := ctx.Value(demandRequestIdentityKey{}).(demandRequestIdentity)
	return identity, ok && identity.clientAddress != ""
}

func withSearchDemandRequestContext(
	next http.Handler,
	config nodeConfig,
	toggles *runtimeToggles,
	access tavilyapi.SearchAccessPolicy,
) http.Handler {
	resolver := httpguard.NewClientAddressResolver(config.TrustedProxies)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if toggles == nil || !toggles.SearchDemandEnabled() || !isDemandSearchRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		address := resolver.Resolve(r)
		parsedAddress := net.ParseIP(address)
		if parsedAddress == nil {
			address = ""
		} else {
			address = parsedAddress.String()
		}
		identity := demandRequestIdentity{
			clientAddress: address,
			authenticated: access.AuthenticatedRead(r) ||
				parsedAddress != nil && parsedAddress.IsLoopback(),
		}
		ctx := context.WithValue(r.Context(), demandRequestIdentityKey{}, identity)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func isDemandSearchRequest(r *http.Request) bool {
	if r.Method == http.MethodGet {
		if r.URL.Query().Get("startRecord") != "" || r.URL.Query().Get("offset") != "" ||
			(r.URL.Query().Get("p") != "" && r.URL.Query().Get("p") != "1") {
			return false
		}
		switch r.URL.Path {
		case yagoproto.PathYaCySearchJSON, yagoproto.PathYaCySearchRSS,
			yagoproto.PathYaCySearchHTML:
			return true
		case "/":
			return strings.TrimSpace(r.URL.Query().Get("q")) != ""
		}
	}
	return r.Method == http.MethodPost && r.URL.Path == tavilyapi.PathSearch
}

type demandCrawlController struct {
	store        *searchdemand.Store
	queue        crawldispatch.CrawlOrderQueue
	documents    documentstore.DocumentDirectory
	initiator    yagomodel.Hash
	toggles      *runtimeToggles
	crawl        seedCrawlOptions
	maximum      func() int
	config       searchdemand.Config
	searcher     searchcore.Searcher
	spawn        func(func()) bool
	mu           sync.RWMutex
	topics       map[string]searchdemand.Topic
	capacity     uint64
	ready        bool
	now          func() time.Time
	lastPrunedAt time.Time
}

type demandCrawlControllerInput struct {
	store     *searchdemand.Store
	queue     crawldispatch.CrawlOrderQueue
	documents documentstore.DocumentDirectory
	initiator yagomodel.Hash
	toggles   *runtimeToggles
	crawl     seedCrawlOptions
	maximum   func() int
}

func newDemandCrawlController(input demandCrawlControllerInput) *demandCrawlController {
	if input.store == nil {
		return nil
	}
	return &demandCrawlController{
		store: input.store, queue: input.queue, documents: input.documents,
		initiator: input.initiator, toggles: input.toggles, crawl: input.crawl,
		maximum: selectMaxPagesPerRunSource([]func() int{input.maximum}),
		config:  searchdemand.DefaultConfig(), spawn: newSwarmSeedAdmission(2).try,
		topics: make(map[string]searchdemand.Topic), now: time.Now,
	}
}

func (c *demandCrawlController) Wrap(searcher searchcore.Searcher) searchcore.Searcher {
	if c == nil || searcher == nil {
		return searcher
	}
	c.searcher = searcher
	return demandSearchDecorator{inner: searcher, controller: c}
}

type demandSearchDecorator struct {
	inner      searchcore.Searcher
	controller *demandCrawlController
}

func (s demandSearchDecorator) Search(
	ctx context.Context,
	req searchcore.Request,
) (searchcore.Response, error) {
	c := s.controller
	identity, marked := demandIdentityFromContext(ctx)
	if !marked || req.Offset != 0 || !c.toggles.SearchDemandEnabled() {
		resp, err := s.inner.Search(ctx, req)
		if err != nil {
			return searchcore.Response{}, fmt.Errorf("run search without demand tracking: %w", err)
		}
		return resp, nil
	}
	now := c.now().UTC()
	cfg := c.config
	cfg.Enabled = true
	topic, accepted, recordErr := c.store.RecordSearch(ctx, searchdemand.RecordSearchInput{
		Query: req.Query, ClientAddress: identity.clientAddress,
		Authenticated: identity.authenticated, At: now, Config: cfg,
	})
	if recordErr != nil {
		slog.WarnContext(ctx, "search demand recording failed", slog.Any("error", recordErr))
	}
	if accepted {
		c.cacheTopic(topic)
	}
	resp, err := s.inner.Search(ctx, req)
	if accepted && err == nil {
		seedContext, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			searchDemandTaskTimeout,
		)
		if !c.spawn(func() {
			defer cancel()
			if seedErr := c.seedTopic(seedContext, topic.ID, resp.Results); seedErr != nil {
				slog.WarnContext(seedContext, "search demand crawl scheduling failed",
					slog.String("topic", topic.ID), slog.Any("error", seedErr))
			}
		}) {
			cancel()
		}
	}
	if err != nil {
		return searchcore.Response{}, fmt.Errorf("run search with demand tracking: %w", err)
	}
	return resp, nil
}

func (c *demandCrawlController) cacheTopic(topic searchdemand.Topic) {
	c.mu.Lock()
	c.topics[topic.ID] = topic
	c.mu.Unlock()
}

func (c *demandCrawlController) updateCachedTopic(ctx context.Context, topicID string) {
	if topicID == "" {
		return
	}
	topic, err := c.store.Topic(ctx, topicID)
	if err == nil && topic.ID != "" {
		c.cacheTopic(topic)
	}
}

func (c *demandCrawlController) refreshSnapshot(ctx context.Context) error {
	now := c.now().UTC()
	topics, err := c.store.Topics(ctx, now, c.config.Retention)
	if err != nil {
		return fmt.Errorf("load search-demand topics: %w", err)
	}
	capacity, err := c.store.MeasuredDailyCapacity(ctx, now)
	if err != nil {
		return fmt.Errorf("load measured crawl capacity: %w", err)
	}
	refreshed := make(map[string]searchdemand.Topic, len(topics))
	for _, topic := range topics {
		refreshed[topic.ID] = topic
	}
	c.mu.Lock()
	c.topics = refreshed
	c.capacity = capacity
	c.ready = true
	c.mu.Unlock()
	return nil
}

func (c *demandCrawlController) topicsSnapshot() ([]searchdemand.Topic, uint64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	topics := make([]searchdemand.Topic, 0, len(c.topics))
	for _, topic := range c.topics {
		topics = append(topics, topic)
	}
	sort.Slice(topics, func(i, j int) bool { return topics[i].ID < topics[j].ID })
	return topics, c.capacity, c.ready
}

func (c *demandCrawlController) budgetFor(
	ctx context.Context,
	topicID string,
	now time.Time,
) (uint64, error) {
	topics, capacity, ready := c.topicsSnapshot()
	var current searchdemand.Topic
	for _, topic := range topics {
		if topic.ID == topicID {
			current = topic
			break
		}
	}
	if current.ID == "" {
		var err error
		current, err = c.store.Topic(ctx, topicID)
		if err != nil {
			return 0, fmt.Errorf("load focused topic budget: %w", err)
		}
		if current.ID == "" {
			return 0, nil
		}
		c.cacheTopic(current)
		topics = append(topics, current)
	}
	if !ready || capacity == 0 {
		day := now.UTC().Format("2006-01-02")
		used := current.ProbeAttempts + current.DailyReserved[day]
		if current.UsefulDocuments == 0 && used < c.config.ProbePageLimit {
			return c.config.ProbePageLimit - used, nil
		}
		return 0, nil
	}
	for _, budget := range searchdemand.Allocate(topics, capacity, 0, now, c.config) {
		if budget.TopicID == topicID {
			return budget.Pages, nil
		}
	}
	return 0, nil
}

func (c *demandCrawlController) seedTopic(
	ctx context.Context,
	topicID string,
	results []searchcore.Result,
) error {
	if c == nil || c.queue == nil || c.documents == nil || !c.toggles.SearchDemandEnabled() {
		return nil
	}
	now := c.now().UTC()
	pages, err := c.budgetFor(ctx, topicID, now)
	if err != nil || pages == 0 {
		return err
	}
	urls, err := c.discoveryURLs(ctx, results)
	if err != nil {
		return err
	}
	if len(urls) == 0 {
		if err := c.store.MarkScheduled(ctx, topicID, now); err != nil {
			return fmt.Errorf("mark topic without crawl candidates scheduled: %w", err)
		}
		return nil
	}
	if len(urls) > searchDemandMaxStarts {
		urls = urls[:searchDemandMaxStarts]
	}
	seedDigest := sha256.Sum256([]byte(strings.Join(urls, "\n")))
	day := now.Format("2006-01-02")
	reservationID := fmt.Sprintf("%s%s-%s-%s", searchDemandProfilePrefix, topicID,
		now.Format("20060102"),
		hex.EncodeToString(seedDigest[:8]))
	reservationState, err := c.store.Reserve(ctx, topicID, day, reservationID, pages)
	if err != nil {
		return fmt.Errorf("reserve focused topic crawl budget: %w", err)
	}
	if reservationState == searchdemand.ReservationBusy {
		return nil
	}
	c.updateCachedTopic(ctx, topicID)
	maximum := int(min(pages, uint64(^uint(0)>>1)))
	if limit := c.maximum(); limit > 0 && limit < maximum {
		maximum = limit
	}
	if maximum < 1 {
		_ = c.store.ReleaseReservation(ctx, reservationID)
		return nil
	}
	order := c.demandCrawlOrder(reservationID, urls, now, maximum)
	duplicate, err := c.queue.PublishOnce(ctx, reservationID, order)
	if err != nil {
		_ = c.store.ReleaseReservation(ctx, reservationID)
		return fmt.Errorf("publish focused search crawl order: %w", err)
	}
	if duplicate && reservationState == searchdemand.ReservationCreated {
		_ = c.store.ReleaseReservation(ctx, reservationID)
	}
	if err := c.store.MarkScheduled(ctx, topicID, now); err != nil {
		return fmt.Errorf("mark focused search topic scheduled: %w", err)
	}
	c.updateCachedTopic(ctx, topicID)
	return nil
}

func (c *demandCrawlController) demandCrawlOrder(
	reservationID string,
	urls []string,
	now time.Time,
	maximum int,
) yagocrawlcontract.CrawlOrder {
	profile := yagocrawlcontract.NewCrawlProfile(yagocrawlcontract.CrawlProfile{
		Name:                reservationID,
		Scope:               yagocrawlcontract.ScopeDomain,
		URLMustMatch:        yagocrawlcontract.MatchAll,
		MaxDepth:            1,
		MaxPagesPerHost:     maximum,
		MaxPagesPerRun:      intPointer(maximum),
		AllowQueryURLs:      c.crawl.AllowQueryURLs,
		IgnoreTLSAuthority:  c.crawl.IgnoreTLSAuthority,
		IgnoreRobots:        c.crawl.IgnoreRobots,
		DisableBrowser:      c.crawl.DisableBrowser,
		FollowNoFollowLinks: c.crawl.FollowNoFollowLinks,
		RecrawlIfOlder:      c.crawl.RecrawlInterval,
	})
	requests := make([]yagocrawlcontract.CrawlRequest, 0, len(urls))
	for _, target := range urls {
		requests = append(requests, yagocrawlcontract.CrawlRequest{
			URL: target, Mode: yagocrawlcontract.CrawlRequestModeURL,
			ProfileHandle: profile.Handle, Initiator: c.initiator, AppDate: now,
		})
	}
	return yagocrawlcontract.CrawlOrder{
		Provenance: mintProvenance(),
		Priority:   yagocrawlcontract.CrawlOrderPriorityAutomaticDiscovery,
		Profile:    profile,
		Requests:   requests,
	}
}

func (c *demandCrawlController) discoveryURLs(
	ctx context.Context,
	results []searchcore.Result,
) ([]string, error) {
	seeds := make([]string, 0, len(results))
	for _, result := range results {
		resultSeeds, err := c.discoverySeeds(ctx, result)
		if err != nil {
			return nil, err
		}
		seeds = append(seeds, resultSeeds...)
	}
	return c.uniqueUnindexedSeeds(ctx, seeds)
}

func (c *demandCrawlController) discoverySeeds(
	ctx context.Context,
	result searchcore.Result,
) ([]string, error) {
	if result.SafetyRating == searchcore.SafetyExplicit {
		return nil, nil
	}
	switch result.Source {
	case searchcore.SourceWeb:
		if c.toggles.SearchDemandWebDiscoveryEnabled() {
			return []string{result.URL}, nil
		}
		return nil, nil
	case searchcore.SourceLocal:
		return c.localResultDiscoverySeeds(ctx, result.URL)
	default:
		return []string{result.URL}, nil
	}
}

func (c *demandCrawlController) localResultDiscoverySeeds(
	ctx context.Context,
	resultURL string,
) ([]string, error) {
	normalized := seedURL(resultURL)
	if normalized == "" {
		return nil, nil
	}
	document, found, err := c.documents.Document(ctx, normalized)
	if err != nil {
		return nil, fmt.Errorf("read local search result links: %w", err)
	}
	if !found {
		return []string{normalized}, nil
	}
	seeds := append([]string(nil), document.Outlinks...)
	for _, anchor := range document.OutboundAnchors {
		seeds = append(seeds, anchor.TargetURL)
	}
	return seeds, nil
}

func (c *demandCrawlController) uniqueUnindexedSeeds(
	ctx context.Context,
	seeds []string,
) ([]string, error) {
	seen := make(map[string]struct{}, len(seeds))
	unique := make([]string, 0, len(seeds))
	for _, raw := range seeds {
		target := seedURL(raw)
		if target == "" {
			continue
		}
		if _, found := seen[target]; found {
			continue
		}
		seen[target] = struct{}{}
		unique = append(unique, target)
	}
	if presence, ok := c.documents.(documentstore.DocumentSetPresence); ok && len(unique) > 0 {
		known, err := presence.DocumentsExist(ctx, unique)
		if err != nil {
			return nil, fmt.Errorf("check demand crawl candidates: %w", err)
		}
		filtered := unique[:0]
		for index, target := range unique {
			if index >= len(known) || !known[index] {
				filtered = append(filtered, target)
			}
		}
		unique = filtered
	}
	return unique, nil
}

func (c *demandCrawlController) Run(ctx context.Context) {
	if c == nil {
		return
	}
	ticker := time.NewTicker(searchDemandRefreshEvery)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if !c.refreshAndSchedule(ctx) && ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *demandCrawlController) refreshAndSchedule(ctx context.Context) bool {
	now := c.now().UTC()
	if c.lastPrunedAt.IsZero() || now.Sub(c.lastPrunedAt) >= searchDemandRefreshEvery {
		if err := c.store.Prune(ctx, now, c.config.Retention); err != nil {
			if ctx.Err() != nil {
				return false
			}
			slog.WarnContext(ctx, "prune search demand state failed", slog.Any("error", err))
		} else {
			c.lastPrunedAt = now
		}
	}
	if err := c.refreshSnapshot(ctx); err != nil {
		if ctx.Err() != nil {
			return false
		}
		slog.WarnContext(ctx, "refresh search demand snapshot failed", slog.Any("error", err))
		return true
	}
	if c.queue != nil && c.searcher != nil && c.toggles.SearchDemandEnabled() {
		c.scheduleDueTopics(ctx)
	}
	return true
}

func startSearchDemandLoop(ctx context.Context, background *sync.WaitGroup, assembled node) {
	if assembled.demand == nil {
		return
	}
	background.Add(1)
	go func() {
		defer background.Done()
		assembled.demand.Run(ctx)
	}()
}

func attachSearchDemandObserver(runtime crawlProcess, controller *demandCrawlController) {
	if controller == nil {
		return
	}
	provider, ok := runtime.(interface{ runRegistry() *crawlruns.Registry })
	if !ok || provider.runRegistry() == nil {
		return
	}
	provider.runRegistry().AddObserver(func(run crawlruns.Run, _ bool, _ int) {
		controller.ObserveRun(
			run,
			run.State == yagocrawlcontract.CrawlRunFinished ||
				run.State == yagocrawlcontract.CrawlRunCancelled,
		)
	})
}

func (c *demandCrawlController) scheduleDueTopics(ctx context.Context) {
	now := c.now().UTC()
	topics, _, _ := c.topicsSnapshot()
	budgets := searchdemand.Allocate(topics, c.capacitySnapshot(), 0, now, c.config)
	byID := make(map[string]uint64, len(budgets))
	for _, budget := range budgets {
		byID[budget.TopicID] = budget.Pages
	}
	for _, topic := range dueDemandTopics(topics, byID, now) {
		if ctx.Err() != nil {
			return
		}
		if err := c.store.MarkScheduled(ctx, topic.ID, now); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.WarnContext(ctx, "mark scheduled search-demand topic failed",
				slog.String("topic", topic.ID), slog.Any("error", err))
			continue
		}
		topic.LastScheduledAt = now
		c.cacheTopic(topic)
		request := searchcore.Request{
			Query: topic.Query, Source: searchcore.SourceGlobal, Limit: 10,
			AllowWebFallback: c.toggles.SearchDemandWebDiscoveryEnabled(),
		}
		resp, err := c.searcher.Search(ctx, request)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.WarnContext(ctx, "scheduled demand search failed",
				slog.String("topic", topic.ID), slog.Any("error", err))
			continue
		}
		if err := c.seedTopic(ctx, topic.ID, resp.Results); err != nil {
			slog.WarnContext(ctx, "scheduled demand crawl failed",
				slog.String("topic", topic.ID), slog.Any("error", err))
		}
	}
}

func dueDemandTopics(
	topics []searchdemand.Topic,
	budgets map[string]uint64,
	now time.Time,
) []searchdemand.Topic {
	sort.Slice(topics, func(i, j int) bool {
		if topics[i].LastScheduledAt.Equal(topics[j].LastScheduledAt) {
			return topics[i].ID < topics[j].ID
		}
		return topics[i].LastScheduledAt.Before(topics[j].LastScheduledAt)
	})
	due := make([]searchdemand.Topic, 0, min(len(topics), searchDemandTopicsPerCycle))
	for _, topic := range topics {
		if budgets[topic.ID] == 0 || !topic.LastScheduledAt.IsZero() &&
			now.Sub(topic.LastScheduledAt) < searchDemandTopicInterval {
			continue
		}
		due = append(due, topic)
		if len(due) == searchDemandTopicsPerCycle {
			break
		}
	}
	return due
}

func (c *demandCrawlController) capacitySnapshot() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.capacity
}

func (c *demandCrawlController) ObserveRun(run crawlruns.Run, terminal bool) {
	if c == nil {
		return
	}
	reservationID := ""
	if strings.HasPrefix(run.ProfileName, searchDemandProfilePrefix) {
		reservationID = run.ProfileName
	}
	ctx, cancel := context.WithTimeout(context.Background(), searchDemandTaskTimeout)
	defer cancel()
	if err := c.store.RecordRunSnapshot(ctx, searchdemand.RunSnapshotInput{
		RunID: run.RunID, ReservationID: reservationID,
		Attempts: run.Tally.FetchAttempts, Indexed: run.Tally.Indexed,
		Terminal: terminal, At: c.now(),
	}); err != nil {
		slog.WarnContext(ctx, "record crawl capacity measurement failed", slog.Any("error", err))
	}
	if reservationID != "" {
		parts := strings.SplitN(
			strings.TrimPrefix(run.ProfileName, searchDemandProfilePrefix),
			"-",
			3,
		)
		if len(parts) >= 1 {
			c.updateCachedTopic(ctx, parts[0])
		}
	}
}

func (c *demandCrawlController) ActivitySnapshot(
	ctx context.Context,
) ([]searchdemand.Topic, uint64, error) {
	if err := c.refreshSnapshot(ctx); err != nil {
		return nil, 0, err
	}
	topics, capacity, _ := c.topicsSnapshot()
	now := c.now().UTC()
	sort.Slice(topics, func(i, j int) bool {
		left := searchdemand.TopicWeight(topics[i], now, c.config.DecayHalfLife)
		right := searchdemand.TopicWeight(topics[j], now, c.config.DecayHalfLife)
		if left == right {
			return topics[i].LastSeen.After(topics[j].LastSeen)
		}
		return left > right
	})
	return topics, capacity, nil
}

func intPointer(value int) *int { return &value }

var _ searchcore.Searcher = demandSearchDecorator{}
