package yagonode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/D4rk4/yago/yagocrawlcontract"
	"github.com/D4rk4/yago/yagonode/internal/adminui"
	"github.com/D4rk4/yago/yagonode/internal/crawlbroker"
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

type importedScheduleWriter interface {
	RecordProfile(
		context.Context,
		yagocrawlcontract.CrawlProfile,
		yagocrawlcontract.CrawlOrderPriority,
	) error
	ScheduleAtIfAbsent(context.Context, string, string, time.Time, time.Duration) (bool, error)
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
	results := make([]adminui.IndexImportResult, 0, len(records))
	for _, record := range records {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("import document batch: %w", ctx.Err())
		default:
		}
		found, conflict, err := s.existingDestination(ctx, record.Document)
		if err != nil {
			return nil, fmt.Errorf("check destination document: %w", err)
		}
		if conflict {
			results = append(results, adminui.IndexImportResult{ID: record.ID, Status: "conflict"})
			continue
		}
		if found {
			if err := s.scheduleImport(ctx, record); err != nil {
				return nil, err
			}
			results = append(
				results,
				adminui.IndexImportResult{ID: record.ID, Status: "already-present"},
			)
			continue
		}
		batch := yagocrawlcontract.IngestBatch{
			SourceURL:     record.Document.NormalizedURL,
			ProfileHandle: s.profile.Handle,
			ObservationID: importObservationID(record.ID),
			ObservedAt:    time.Now().UTC(),
			Document:      record.Document,
		}
		if err := s.ingest.SubmitImport(ctx, batch, s.profile); err != nil {
			return nil, fmt.Errorf("ingest imported document: %w", err)
		}
		if err := s.scheduleImport(ctx, record); err != nil {
			return nil, err
		}
		results = append(results, adminui.IndexImportResult{ID: record.ID, Status: "imported"})
	}

	return results, nil
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
