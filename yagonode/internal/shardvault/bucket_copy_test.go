package shardvault_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/D4rk4/yago/yagonode/internal/shardvault"
	"github.com/D4rk4/yago/yagonode/internal/vault"
)

type bucketCopyStringCodec struct{}

func (bucketCopyStringCodec) Encode(value string) ([]byte, error) { return []byte(value), nil }
func (bucketCopyStringCodec) Decode(value []byte) (string, error) { return string(value), nil }

func openBucketCopyVault(t *testing.T) *vault.Vault {
	t.Helper()
	storage, err := shardvault.Open(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("open shard vault: %v", err)
	}
	t.Cleanup(func() {
		if err := storage.Close(); err != nil {
			t.Errorf("close shard vault: %v", err)
		}
	})
	return storage
}

type bucketCopyRecord struct {
	key   vault.Key
	value string
}

func TestCopyBucketFromIsBoundedIdempotentAndPreservesDestinationValues(t *testing.T) {
	ctx := context.Background()
	source := openBucketCopyVault(t)
	destination := openBucketCopyVault(t)
	sourceRecords := registerBucketCopyRecords(t, source)
	destinationRecords := registerBucketCopyRecords(t, destination)
	writeBucketCopyRecords(t, source, sourceRecords, []bucketCopyRecord{
		{key: vault.Key("a"), value: "source-a"},
		{key: vault.Key("b"), value: "source-b"},
		{key: vault.Key("c"), value: "source-c"},
	})
	writeBucketCopyRecords(t, destination, destinationRecords, []bucketCopyRecord{
		{key: vault.Key("b"), value: "destination-b"},
	})

	if err := destination.CopyBucketFrom(ctx, source, "records", 2); err != nil {
		t.Fatalf("copy records: %v", err)
	}
	if err := destination.CopyBucketFrom(ctx, source, "records", 2); err != nil {
		t.Fatalf("repeat records copy: %v", err)
	}
	assertBucketCopyRecords(t, destination, destinationRecords, []bucketCopyRecord{
		{key: vault.Key("a"), value: "source-a"},
		{key: vault.Key("b"), value: "destination-b"},
		{key: vault.Key("c"), value: "source-c"},
	})
	assertBucketCopyRecords(t, source, sourceRecords, []bucketCopyRecord{
		{key: vault.Key("a"), value: "source-a"},
		{key: vault.Key("b"), value: "source-b"},
		{key: vault.Key("c"), value: "source-c"},
	})
}

func registerBucketCopyRecords(t *testing.T, storage *vault.Vault) *vault.Collection[string] {
	t.Helper()
	records, err := vault.Register(storage, "records", bucketCopyStringCodec{})
	if err != nil {
		t.Fatalf("register records: %v", err)
	}
	return records
}

func writeBucketCopyRecords(
	t *testing.T,
	storage *vault.Vault,
	records *vault.Collection[string],
	values []bucketCopyRecord,
) {
	t.Helper()
	for _, record := range values {
		if err := storage.Update(context.Background(), func(tx *vault.Txn) error {
			if err := records.Put(tx, record.key, record.value); err != nil {
				return fmt.Errorf("put record %q: %w", record.key, err)
			}
			return nil
		}); err != nil {
			t.Fatalf("write record %q: %v", record.key, err)
		}
	}
}

func assertBucketCopyRecords(
	t *testing.T,
	storage *vault.Vault,
	records *vault.Collection[string],
	want []bucketCopyRecord,
) {
	t.Helper()
	if err := storage.View(context.Background(), func(tx *vault.Txn) error {
		length, err := records.Len(tx)
		if err != nil {
			return fmt.Errorf("count records: %w", err)
		}
		if length != len(want) {
			t.Fatalf("record length = %d, want %d", length, len(want))
		}
		for _, record := range want {
			got, found, err := records.Get(tx, record.key)
			if err != nil {
				return fmt.Errorf("read record %q: %w", record.key, err)
			}
			if !found || got != record.value {
				t.Fatalf("record %q = %q, found=%t; want %q", record.key, got, found, record.value)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read records: %v", err)
	}
}

func TestCopyBucketFromRejectsInvalidInputsAndAllowsSelfCopy(t *testing.T) {
	ctx := context.Background()
	storage := openBucketCopyVault(t)
	if err := storage.CopyBucketFrom(ctx, storage, "records", 1); err != nil {
		t.Fatalf("self copy: %v", err)
	}
	for _, size := range []int{0, -1, 4097} {
		if err := storage.CopyBucketFrom(ctx, openBucketCopyVault(t), "records", size); err == nil {
			t.Fatalf("page size %d was accepted", size)
		}
	}
	if err := storage.CopyBucketFrom(ctx, nil, "records", 1); err == nil {
		t.Fatal("nil source was accepted")
	}
}

func TestPutBucketValueRejectsReadOnlyTransactions(t *testing.T) {
	storage := openBucketCopyVault(t)
	if _, err := vault.Register(storage, "records", bucketCopyStringCodec{}); err != nil {
		t.Fatalf("register records: %v", err)
	}
	if err := storage.View(context.Background(), func(tx *vault.Txn) error {
		return tx.PutBucketValue("records", vault.Key("key"), []byte("value"))
	}); err == nil {
		t.Fatal("raw bucket write succeeded inside a read transaction")
	}
}
