package yagonode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/D4rk4/yago/yagocrawlcontract"
	"github.com/D4rk4/yago/yagonode/internal/adminui"
	"github.com/D4rk4/yago/yagonode/internal/crawlbroker"
	"github.com/D4rk4/yago/yagonode/internal/documentimport"
	"github.com/D4rk4/yago/yagonode/internal/documentstore"
	"github.com/D4rk4/yago/yagonode/internal/recrawlfrontier"
)

const importedRecrawlRetryInterval = time.Hour

type documentImportService struct {
	documents       documentstore.DocumentDirectory
	ingest          documentImportSubmitter
	frontier        importedScheduleWriter
	profile         yagocrawlcontract.CrawlProfile
	profileMu       sync.Mutex
	profileRecorded bool
}

type documentImportSubmitter interface {
	SubmitImport(
		context.Context,
		yagocrawlcontract.IngestBatch,
		yagocrawlcontract.CrawlProfile,
	) error
}

type indexedImportRecord struct {
	index  int
	record adminui.IndexImportRecord
}

type importedScheduleWriter interface {
	RecordProfile(
		context.Context,
		yagocrawlcontract.CrawlProfile,
		yagocrawlcontract.CrawlOrderPriority,
	) error
	ScheduleAtIfAbsent(context.Context, string, string, time.Time, time.Duration) (bool, error)
}

type importedScheduleBatchWriter interface {
	ScheduleRecrawlsAtIfAbsent(
		context.Context,
		string,
		[]recrawlfrontier.RecrawlSchedule,
		time.Duration,
	) error
}

func newDocumentImportService(
	documents documentstore.DocumentDirectory,
	ingest *crawlbroker.IngestReceiver,
	frontier *recrawlfrontier.Frontier,
) *documentImportService {
	profile := yagocrawlcontract.NewCrawlProfile(yagocrawlcontract.CrawlProfile{
		Name:            "Document migration recrawl",
		Scope:           yagocrawlcontract.ScopeWide,
		URLMustMatch:    yagocrawlcontract.MatchAll,
		MaxPagesPerHost: 1,
		AllowQueryURLs:  true,
		Formats:         yagocrawlcontract.DefaultFormatToggles(),
	})
	return &documentImportService{
		documents: documents,
		ingest:    ingest,
		frontier:  frontier,
		profile:   profile,
	}
}

func (s *documentImportService) ImportDocuments(
	ctx context.Context,
	records []adminui.IndexImportRecord,
) ([]adminui.IndexImportResult, error) {
	if err := s.ensureProfile(ctx); err != nil {
		return nil, fmt.Errorf("register document migration profile: %w", err)
	}
	if len(records) == 0 {
		return []adminui.IndexImportResult{}, nil
	}
	workerCount := documentImportWorkerCount(s.ingest, len(records))
	queues := make([]chan indexedImportRecord, workerCount)
	queueCapacity := (len(records) + workerCount - 1) / workerCount
	for index := range queues {
		queues[index] = make(chan indexedImportRecord, queueCapacity)
	}
	results := make([]adminui.IndexImportResult, len(records))
	importContext, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	var firstErr error
	var failOnce sync.Once
	for _, queue := range queues {
		workers.Add(1)
		go func(jobs <-chan indexedImportRecord) {
			defer workers.Done()
			for job := range jobs {
				result, err := s.importDocument(importContext, job.record)
				if err != nil {
					failOnce.Do(func() {
						firstErr = err
						cancel()
					})
					return
				}
				results[job.index] = result
			}
		}(queue)
	}
	for index, record := range records {
		queue := queues[importWorkerForURL(record.Document.NormalizedURL, workerCount)]
		select {
		case queue <- indexedImportRecord{index: index, record: record}:
		case <-importContext.Done():
			break
		}
		if importContext.Err() != nil {
			break
		}
	}
	for _, queue := range queues {
		close(queue)
	}
	workers.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("import document batch: %w", err)
	}
	if err := s.scheduleImportBatch(ctx, records, results); err != nil {
		return nil, err
	}
	return results, nil
}

func documentImportWorkerCount(submitter documentImportSubmitter, recordCount int) int {
	maximum := 1
	if concurrent, ok := submitter.(documentimport.ConcurrentSubmitter); ok {
		maximum = concurrent.MaximumConcurrentImports()
	}
	if maximum < 1 {
		maximum = 1
	}
	if maximum > yagocrawlcontract.MaximumIngestBatchRecords {
		maximum = yagocrawlcontract.MaximumIngestBatchRecords
	}
	return min(recordCount, maximum)
}

func importWorkerForURL(rawURL string, workerCount int) int {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(rawURL))
	return int(hash.Sum32() % uint32(workerCount))
}

func (s *documentImportService) importDocument(
	ctx context.Context,
	record adminui.IndexImportRecord,
) (adminui.IndexImportResult, error) {
	if err := ctx.Err(); err != nil {
		return adminui.IndexImportResult{}, fmt.Errorf("import document batch: %w", err)
	}
	found, conflict, err := s.existingDestination(ctx, record.Document)
	if err != nil {
		return adminui.IndexImportResult{}, fmt.Errorf("check destination document: %w", err)
	}
	if conflict {
		return adminui.IndexImportResult{ID: record.ID, Status: "conflict"}, nil
	}
	if found {
		return adminui.IndexImportResult{ID: record.ID, Status: "already-present"}, nil
	}
	batch := yagocrawlcontract.IngestBatch{
		SourceURL:     record.Document.NormalizedURL,
		ProfileHandle: s.profile.Handle,
		ObservationID: importObservationID(record.ID),
		ObservedAt:    time.Now().UTC(),
		Document:      record.Document,
	}
	if err := s.ingest.SubmitImport(ctx, batch, s.profile); err != nil {
		return adminui.IndexImportResult{}, fmt.Errorf("ingest imported document: %w", err)
	}
	return adminui.IndexImportResult{ID: record.ID, Status: "imported"}, nil
}

func (s *documentImportService) scheduleImportBatch(
	ctx context.Context,
	records []adminui.IndexImportRecord,
	results []adminui.IndexImportResult,
) error {
	schedules := make([]recrawlfrontier.RecrawlSchedule, 0, len(records))
	for index, record := range records {
		if record.NextVisitAt == nil ||
			(results[index].Status != "imported" && results[index].Status != "already-present") {
			continue
		}
		schedules = append(schedules, recrawlfrontier.RecrawlSchedule{
			URL:   record.Document.NormalizedURL,
			DueAt: *record.NextVisitAt,
		})
	}
	if len(schedules) == 0 {
		return nil
	}
	if batchWriter, ok := s.frontier.(importedScheduleBatchWriter); ok {
		if err := batchWriter.ScheduleRecrawlsAtIfAbsent(
			ctx,
			s.profile.Handle,
			schedules,
			importedRecrawlRetryInterval,
		); err != nil {
			return fmt.Errorf("schedule imported recrawls: %w", err)
		}

		return nil
	}
	for index, record := range records {
		if record.NextVisitAt == nil ||
			(results[index].Status != "imported" && results[index].Status != "already-present") {
			continue
		}
		if err := s.scheduleImport(ctx, record); err != nil {
			return err
		}
	}

	return nil
}

func (s *documentImportService) ensureProfile(ctx context.Context) error {
	s.profileMu.Lock()
	defer s.profileMu.Unlock()
	if s.profileRecorded {
		return nil
	}
	if err := s.frontier.RecordProfile(
		ctx,
		s.profile,
		yagocrawlcontract.CrawlOrderPriorityNormal,
	); err != nil {
		return fmt.Errorf("record document migration profile: %w", err)
	}
	s.profileRecorded = true
	return nil
}

func (s *documentImportService) existingDestination(
	ctx context.Context,
	document yagocrawlcontract.DocumentIngest,
) (bool, bool, error) {
	existing, found, err := s.documents.Document(ctx, document.NormalizedURL)
	if err != nil || !found {
		if err != nil {
			return false, false, fmt.Errorf("read existing normalized URL: %w", err)
		}
		return found, false, nil
	}
	return true, existing.ContentHash == "" || existing.ContentHash != document.ContentHash, nil
}

func (s *documentImportService) scheduleImport(
	ctx context.Context,
	record adminui.IndexImportRecord,
) error {
	if record.NextVisitAt == nil {
		return nil
	}
	if _, err := s.frontier.ScheduleAtIfAbsent(
		ctx,
		record.Document.NormalizedURL,
		s.profile.Handle,
		*record.NextVisitAt,
		importedRecrawlRetryInterval,
	); err != nil {
		return fmt.Errorf("store imported recrawl schedule: %w", err)
	}
	return nil
}

func importObservationID(recordID string) string {
	sum := sha256.Sum256([]byte(recordID))
	return "migration-" + hex.EncodeToString(sum[:])
}

var _ adminui.IndexImporter = (*documentImportService)(nil)
