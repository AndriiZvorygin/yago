package yagonode

import (
	"context"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/adminui"
	"github.com/D4rk4/yago/yagonode/internal/searchactivity"
	"github.com/D4rk4/yago/yagonode/internal/searchdemand"
)

const activityTopWords = 20

type activitySource struct {
	tracker *searchactivity.Tracker
	demand  *demandCrawlController
}

func newActivitySource(
	tracker *searchactivity.Tracker,
	demand *demandCrawlController,
) adminui.ActivitySource {
	if tracker == nil && demand == nil {
		return nil
	}
	return activitySource{tracker: tracker, demand: demand}
}

func (s activitySource) Activity(ctx context.Context) adminui.ActivityView {
	view := adminui.ActivityView{Mode: string(searchactivity.ModeOff)}
	if s.tracker != nil {
		s.populateSearchActivity(&view)
	}
	if s.demand != nil {
		s.populateDemandActivity(ctx, &view)
	}
	return view
}

func (s activitySource) populateSearchActivity(view *adminui.ActivityView) {
	entries, total, zero := s.tracker.Snapshot()
	view.Mode = string(s.tracker.Mode())
	view.Total = total
	view.ConfirmedZeroResults = zero
	view.Entries = make([]adminui.ActivityEntry, 0, len(entries))
	for _, entry := range entries {
		view.Entries = append(view.Entries, adminui.ActivityEntry{
			Time:         entry.At.UTC().Format("2006-01-02 15:04:05 UTC"),
			Query:        entry.Query,
			Length:       entry.QueryLength,
			Terms:        entry.Terms,
			Results:      entry.Results,
			ResultsKnown: !entry.Failed,
			Complete:     !entry.Failed && !entry.Incomplete,
			Duration:     entry.Duration.Round(time.Millisecond).String(),
			Source:       entry.Source,
		})
	}
	for _, word := range s.tracker.TopWords(activityTopWords) {
		view.TopWords = append(view.TopWords, adminui.ActivityWord{
			Word: word.Word, Count: word.Count,
		})
	}
}

func (s activitySource) populateDemandActivity(ctx context.Context, view *adminui.ActivityView) {
	view.DemandEnabled = s.demand.toggles.SearchDemandEnabled()
	view.DemandWebDiscovery = s.demand.toggles.SearchDemandWebDiscoveryEnabled()
	topics, capacity, err := s.demand.ActivitySnapshot(ctx)
	if err != nil {
		view.DemandError = err.Error()
		return
	}
	view.DemandCapacity = capacity
	view.DemandTopics = demandActivityTopics(topics, s.demand.now().UTC())
}

func demandActivityTopics(
	topics []searchdemand.Topic,
	now time.Time,
) []adminui.ActivityDemandTopic {
	day := now.Format("2006-01-02")
	result := make([]adminui.ActivityDemandTopic, 0, min(len(topics), 50))
	for _, topic := range topics {
		result = append(result, adminui.ActivityDemandTopic{
			Query: topic.Query, Searches: topicSearchCount(topic),
			Weight:        searchdemand.TopicWeight(topic, now, 7*24*time.Hour),
			FetchAttempts: topic.DailyAttempts[day], UsefulDocs: topic.UsefulDocuments,
			LastSearch:    topic.LastSeen.UTC().Format("2006-01-02 15:04 UTC"),
			LastScheduled: formatDemandSchedule(topic.LastScheduledAt),
			ProbeOnly:     topic.UsefulDocuments == 0,
		})
		if len(result) == 50 {
			break
		}
	}
	return result
}

func topicSearchCount(topic searchdemand.Topic) int {
	var count uint64
	for _, event := range topic.Events {
		if event.Count == 0 {
			count++
			continue
		}
		count += uint64(event.Count)
	}
	return int(count)
}

func formatDemandSchedule(at time.Time) string {
	if at.IsZero() {
		return "not yet"
	}
	return at.UTC().Format("2006-01-02 15:04 UTC")
}
