package crawlbroker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagocrawlcontract"
	"github.com/D4rk4/yago/yagonode/internal/crawlresults"
)

func TestSubmitImportUsesBoundedNormalIngestDelivery(t *testing.T) {
	receiver := newIngestReceiver()
	profile := yagocrawlcontract.NewCrawlProfile(
		yagocrawlcontract.CrawlProfile{Name: "migration fixture"},
	)
	document := yagocrawlcontract.DocumentIngest{
		NormalizedURL: "https://example.test/page",
		ExtractedText: "sanitized migration fixture",
	}
	batch := yagocrawlcontract.IngestBatch{
		SourceURL:     document.NormalizedURL,
		ProfileHandle: profile.Handle,
		Document:      document,
	}
	result := make(chan error, 1)
	go func() {
		result <- receiver.SubmitImport(context.Background(), batch, profile)
	}()
	delivery := <-receiver.Receive()
	if !delivery.ImportOnly || delivery.CrawlProfile == nil ||
		delivery.CrawlProfile.Handle != profile.Handle || delivery.BatchJSONSize <= len(document.ExtractedText) {
		t.Fatalf("migration delivery lost ingest ownership or size accounting: %+v", delivery)
	}
	if err := delivery.Ack(context.Background()); err != nil {
		t.Fatalf("ack import delivery: %v", err)
	}
	if err := <-result; err != nil {
		t.Fatalf("SubmitImport: %v", err)
	}
	if receiver.Outstanding() != 0 {
		t.Fatalf("outstanding import deliveries = %d, want zero", receiver.Outstanding())
	}
}

func TestSubmitImportRejectsMismatchedProfile(t *testing.T) {
	receiver := newIngestReceiver()
	profile := yagocrawlcontract.NewCrawlProfile(
		yagocrawlcontract.CrawlProfile{Name: "migration fixture"},
	)
	batch := yagocrawlcontract.IngestBatch{ProfileHandle: "different"}
	if err := receiver.SubmitImport(context.Background(), batch, profile); err == nil {
		t.Fatal("SubmitImport accepted a profile handle mismatch")
	}
	if receiver.Outstanding() != 0 {
		t.Fatalf("outstanding after rejected import = %d, want zero", receiver.Outstanding())
	}
}

func TestSubmitImportRejectsUnencodableBatch(t *testing.T) {
	receiver := newIngestReceiver()
	profile := yagocrawlcontract.NewCrawlProfile(
		yagocrawlcontract.CrawlProfile{Name: "migration fixture"},
	)
	batch := yagocrawlcontract.IngestBatch{
		ProfileHandle: profile.Handle,
		Document: yagocrawlcontract.DocumentIngest{
			NormalizedURL: "https://example.test/",
			FetchedAt:     time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	if err := receiver.SubmitImport(context.Background(), batch, profile); err == nil {
		t.Fatal("SubmitImport accepted a batch whose timestamp cannot be encoded as JSON")
	}
	if receiver.Outstanding() != 0 {
		t.Fatalf("outstanding after encode failure = %d, want zero", receiver.Outstanding())
	}
}

func TestSubmitImportCancellationBeforeDeliveryReleasesOutstanding(t *testing.T) {
	receiver := newIngestReceiver()
	profile := yagocrawlcontract.NewCrawlProfile(
		yagocrawlcontract.CrawlProfile{Name: "migration fixture"},
	)
	batch := yagocrawlcontract.IngestBatch{ProfileHandle: profile.Handle}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := receiver.SubmitImport(ctx, batch, profile); !errors.Is(err, context.Canceled) {
		t.Fatalf("SubmitImport error = %v, want context canceled", err)
	}
	if receiver.Outstanding() != 0 {
		t.Fatalf("outstanding after canceled submission = %d, want zero", receiver.Outstanding())
	}
}

func TestSubmitImportRejectAndDeferReturnDeliveryOutcome(t *testing.T) {
	tests := []struct {
		name      string
		complete  func(delivery crawlresults.IngestDelivery) error
		wantError string
	}{
		{
			name: "reject",
			complete: func(delivery crawlresults.IngestDelivery) error {
				return delivery.Reject(context.Background(), "invalid fixture")
			},
			wantError: "import rejected: invalid fixture",
		},
		{
			name: "defer",
			complete: func(delivery crawlresults.IngestDelivery) error {
				return delivery.Nak(context.Background())
			},
			wantError: errIngestDeferred.Error(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			receiver := newIngestReceiver()
			profile := yagocrawlcontract.NewCrawlProfile(
				yagocrawlcontract.CrawlProfile{Name: "migration fixture"},
			)
			batch := yagocrawlcontract.IngestBatch{ProfileHandle: profile.Handle}
			result := make(chan error, 1)
			go func() { result <- receiver.SubmitImport(context.Background(), batch, profile) }()
			delivery := <-receiver.Receive()
			if err := test.complete(delivery); err != nil {
				t.Fatalf("complete delivery: %v", err)
			}
			if err := <-result; err == nil || err.Error() != test.wantError {
				t.Fatalf("SubmitImport result = %v, want %q", err, test.wantError)
			}
			if receiver.Outstanding() != 0 {
				t.Fatalf("outstanding after %s = %d, want zero", test.name, receiver.Outstanding())
			}
		})
	}
}

func TestSubmitImportCancellationWhileWaitingCanStillBeAcknowledged(t *testing.T) {
	receiver := newIngestReceiver()
	profile := yagocrawlcontract.NewCrawlProfile(
		yagocrawlcontract.CrawlProfile{Name: "migration fixture"},
	)
	batch := yagocrawlcontract.IngestBatch{ProfileHandle: profile.Handle}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- receiver.SubmitImport(ctx, batch, profile) }()
	delivery := <-receiver.Receive()
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("SubmitImport error = %v, want context canceled", err)
	}
	if receiver.Outstanding() != 1 {
		t.Fatalf("outstanding before delivery completion = %d, want one", receiver.Outstanding())
	}
	if err := delivery.Ack(context.Background()); err != nil {
		t.Fatalf("ack canceled delivery: %v", err)
	}
	if receiver.Outstanding() != 0 {
		t.Fatalf("outstanding after acknowledgment = %d, want zero", receiver.Outstanding())
	}
}
