package shardvault

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/D4rk4/yago/yagonode/internal/vault"
)

func TestUpdateCommitsOnlyShardsHoldingWrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vault")
	e := openPendingWritesEngine(t, dir)
	stable, changing := keysOnDistinctShards(e)
	putRecords(t, e, map[string]string{stable: "stable", changing: "before"})
	stableShard := e.route(testBucket, vault.Key(stable))
	missing := keyOnShard(e, stableShard, stable)

	generations := shardWriteActivities(t, e)
	updateRecords(t, e, func(bucket vault.EngineBucket) error {
		bucket.Get(vault.Key(stable))
		bucket.Get(vault.Key(changing))

		return nil
	})
	assertAdvancedShards(t, generations, shardWriteActivities(t, e), nil)

	updateRecords(t, e, func(bucket vault.EngineBucket) error {
		if err := bucket.Put(vault.Key(stable), []byte("stable")); err != nil {
			return fmt.Errorf("identical put: %w", err)
		}
		if err := bucket.Delete(vault.Key(missing)); err != nil {
			return fmt.Errorf("absent delete: %w", err)
		}

		return bucket.Put(vault.Key(changing), []byte("after"))
	})
	changedShard := e.route(testBucket, vault.Key(changing))
	after := shardWriteActivities(t, e)
	assertAdvancedShards(t, generations, after, []int{changedShard})

	updateRecords(t, e, func(bucket vault.EngineBucket) error {
		return bucket.Delete(vault.Key(stable))
	})
	assertAdvancedShards(t, after, shardWriteActivities(t, e), []int{stableShard})

	if err := e.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened := openPendingWritesEngine(t, dir)
	assertSameRecords(t, map[string]string{changing: "after"}, readAllRecords(t, reopened))
}

func openPendingWritesEngine(t *testing.T, dir string) *engine {
	t.Helper()
	e, err := openEngine(dir, 1<<20)
	if err != nil {
		t.Fatalf("openEngine: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if err := e.Provision(testBucket); err != nil {
		t.Fatalf("provision: %v", err)
	}

	return e
}

func keysOnDistinctShards(e *engine) (string, string) {
	first := "record-0"

	return first, keyAvoidingShard(e, e.route(testBucket, vault.Key(first)))
}

func keyAvoidingShard(e *engine, shard int) string {
	for index := 1; ; index++ {
		key := fmt.Sprintf("record-%d", index)
		if e.route(testBucket, vault.Key(key)) != shard {
			return key
		}
	}
}

func keyOnShard(e *engine, shard int, exclude string) string {
	for index := 0; ; index++ {
		key := fmt.Sprintf("absent-%d", index)
		if key != exclude && e.route(testBucket, vault.Key(key)) == shard {
			return key
		}
	}
}

func updateRecords(t *testing.T, e *engine, change func(vault.EngineBucket) error) {
	t.Helper()
	err := e.Update(context.Background(), func(txn vault.EngineTxn) error {
		return change(txn.Bucket(testBucket))
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
}

type shardWriteActivity struct {
	generation int
	pageWrites int64
}

func shardWriteActivities(t *testing.T, e *engine) []shardWriteActivity {
	t.Helper()
	activity := make([]shardWriteActivity, len(e.shards))
	for index, shard := range e.shards {
		if err := shard.View(func(tx *bolt.Tx) error {
			activity[index].generation = tx.ID()

			return nil
		}); err != nil {
			t.Fatalf("read shard %d generation: %v", index, err)
		}
		stats := shard.Stats().TxStats
		activity[index].pageWrites = stats.GetWrite()
	}

	return activity
}

func assertAdvancedShards(t *testing.T, before, after []shardWriteActivity, want []int) {
	t.Helper()
	var committed, written []int
	for index := range before {
		if after[index].generation != before[index].generation {
			committed = append(committed, index)
		}
		if after[index].pageWrites != before[index].pageWrites {
			written = append(written, index)
		}
	}
	if !slices.Equal(committed, want) {
		t.Fatalf("committed shards = %v, want %v", committed, want)
	}
	if !slices.Equal(written, want) {
		t.Fatalf("written shards = %v, want %v", written, want)
	}
}
