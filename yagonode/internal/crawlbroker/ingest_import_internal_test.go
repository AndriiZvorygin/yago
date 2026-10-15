package crawlbroker

import (
	"context"
	"testing"

	"github.com/D4rk4/yago/yagocrawlcontract"
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
