package shardvault

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/D4rk4/yago/yagonode/internal/vault"
)

func TestPutSkipsOnlyIdenticalStoredValues(t *testing.T) {
	e := openTestEngine(t)
	short := writeRecords(t, e, 64)
	compressible := map[string]string{}
	for index := range 16 {
		compressible[fmt.Sprintf("long-%02d", index)] = strings.Repeat("compressible ", 32)
	}
	putRecords(t, e, compressible)

	if allocated := dirtiedPageBytes(t, e, short); allocated != 0 {
		t.Fatalf("identical short values dirtied %d bytes", allocated)
	}
	if allocated := dirtiedPageBytes(t, e, compressible); allocated != 0 {
		t.Fatalf("identical compressed values dirtied %d bytes", allocated)
	}

	changed := make(map[string]string, len(short))
	for key, value := range short {
		changed[key] = value + "-changed"
	}
	if allocated := dirtiedPageBytes(t, e, changed); allocated == 0 {
		t.Fatal("changed values dirtied no pages")
	}
	assertSameRecords(t, mergedRecords(changed, compressible), readAllRecords(t, e))
}

func putRecords(t *testing.T, e *engine, records map[string]string) {
	t.Helper()
	err := e.Update(context.Background(), func(txn vault.EngineTxn) error {
		bucket := txn.Bucket(testBucket)
		for key, value := range records {
			if err := bucket.Put(vault.Key(key), []byte(value)); err != nil {
				return fmt.Errorf("put %s: %w", key, err)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("put records: %v", err)
	}
}

func dirtiedPageBytes(t *testing.T, e *engine, records map[string]string) int64 {
	t.Helper()
	before := allocatedPageBytes(e)
	putRecords(t, e, records)

	return allocatedPageBytes(e) - before
}

func allocatedPageBytes(e *engine) int64 {
	var allocated int64
	for _, shard := range e.shards {
		stats := shard.Stats().TxStats
		allocated += stats.GetPageAlloc()
	}

	return allocated
}

func mergedRecords(first, second map[string]string) map[string]string {
	merged := make(map[string]string, len(first)+len(second))
	for key, value := range first {
		merged[key] = value
	}
	for key, value := range second {
		merged[key] = value
	}

	return merged
}
