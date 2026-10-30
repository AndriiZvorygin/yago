package searchindex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/index/scorch"
)

func TestReopenedShardKeepsMidSizeSegmentBesideOneDocumentFlush(t *testing.T) {
	shard, path := reopenShardStoredWithoutMergeFloor(t)
	indexSyntheticDocuments(t, shard, "mid", 4000)
	requireSingleSegmentBetweenMergeFloorAndInMemoryCap(t, shard, path)
	indexSyntheticDocuments(t, shard, "flush", 1)

	stats := waitForScorchMergerAtRoot(t, shard)
	if merged := stats["TotFileMergeSegments"].(uint64); merged != 0 {
		t.Fatalf("one-document flush merged %d segments, want the mid-size segment kept", merged)
	}
	if segments := stats["num_root_filesegments"].(uint64); segments != 2 {
		t.Fatalf("root file segments = %d, want 2", segments)
	}
}

func TestReopenedShardStillMergesAccumulatedFlushes(t *testing.T) {
	shard, path := reopenShardStoredWithoutMergeFloor(t)
	indexSyntheticDocuments(t, shard, "mid", 4000)
	requireSingleSegmentBetweenMergeFloorAndInMemoryCap(t, shard, path)
	for flush := range 3 {
		indexSyntheticDocuments(t, shard, fmt.Sprintf("flush-%d", flush), 1)
	}

	stats := waitForScorchMergerAtRoot(t, shard)
	if merged := stats["TotFileMergeSegments"].(uint64); merged == 0 {
		t.Fatal("accumulated flushes were never merged")
	}
	if segments := stats["num_root_filesegments"].(uint64); segments >= 4 {
		t.Fatalf("root file segments = %d, want fewer than 4", segments)
	}
}

func TestOpenBleveShardWithCurrentPolicyRefusesPathWithoutShard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search.idx")
	if err := os.WriteFile(path, []byte("not an index"), 0o600); err != nil {
		t.Fatal(err)
	}

	if shard, err := openBleveShardWithCurrentPolicy(path); shard != nil || err == nil {
		t.Fatalf("shard=%v error=%v, want refusal", shard, err)
	}
}

func reopenShardStoredWithoutMergeFloor(t *testing.T) (bleve.Index, string) {
	t.Helper()
	indexMapping, err := newSearchIndexMapping()
	if err != nil {
		t.Fatal(err)
	}
	storedConfiguration := bleveDiskScorchConfiguration()
	delete(
		storedConfiguration["scorchMergePlanOptions"].(map[string]interface{}),
		"FloorSegmentFileSize",
	)
	path := filepath.Join(t.TempDir(), "search.idx")
	created, err := bleve.NewUsing(
		path,
		indexMapping,
		scorch.Name,
		scorch.Name,
		storedConfiguration,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	shard, err := openBleveDisk(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shard.Close() })

	return shard, path
}

func indexSyntheticDocuments(t *testing.T, shard bleve.Index, prefix string, documents int) {
	t.Helper()
	batch := shard.NewBatch()
	for document := range documents {
		var body strings.Builder
		for word := range 120 {
			fmt.Fprintf(&body, "w%d ", (document*7919+word*104729)%60000)
		}
		if err := batch.Index(
			fmt.Sprintf("https://example.org/%s/%d", prefix, document),
			bleveDocument{Body: body.String(), Analyzer: standardTextAnalyzer},
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := shard.Batch(batch); err != nil {
		t.Fatal(err)
	}
}

func requireSingleSegmentBetweenMergeFloorAndInMemoryCap(
	t *testing.T,
	shard bleve.Index,
	path string,
) {
	t.Helper()
	stats := waitForScorchMergerAtRoot(t, shard)
	if segments := stats["num_root_filesegments"].(uint64); segments != 1 {
		t.Fatalf("root file segments = %d, want 1", segments)
	}
	files, err := filepath.Glob(filepath.Join(path, "store", "*.zap"))
	if err != nil || len(files) != 1 {
		t.Fatalf("segment files = %v, error = %v", files, err)
	}
	info, err := os.Stat(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= diskMergeFloorSegmentBytes || info.Size() >= diskInMemoryMergeBytes {
		t.Fatalf(
			"segment bytes = %d, want between merge floor and in-memory merge cap",
			info.Size(),
		)
	}
}

func waitForScorchMergerAtRoot(t *testing.T, shard bleve.Index) map[string]interface{} {
	t.Helper()
	advanced, err := shard.Advanced()
	if err != nil {
		t.Fatal(err)
	}
	scorchIndex, ok := advanced.(*scorch.Scorch)
	if !ok {
		t.Fatalf("advanced index type = %T", advanced)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		stats := scorchIndex.StatsMap()
		root := stats["CurRootEpoch"].(uint64)
		if stats["LastPersistedEpoch"].(uint64) == root &&
			stats["LastMergedEpoch"].(uint64) == root {
			return stats
		}
		if time.Now().After(deadline) {
			t.Fatalf("merger did not reach root epoch %d", root)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
