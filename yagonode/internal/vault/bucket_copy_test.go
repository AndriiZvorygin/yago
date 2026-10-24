package vault_test

import (
	"context"
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

func TestCopyBucketFromIsBoundedIdempotentAndPreservesDestinationValues(t *testing.T) {
	ctx := context.Background()
	source := openBucketCopyVault(t)
	destination := openBucketCopyVault(t)
	sourceRecords, err := vault.Register(source, "records", bucketCopyStringCodec{})
	if err != nil {
		t.Fatalf("register source records: %v", err)
	}
	destinationRecords, err := vault.Register(destination, "records", bucketCopyStringCodec{})
	if err != nil {
		t.Fatalf("register destination records: %v", err)
	}
	for _, record := range []struct {
		key   vault.Key
		value string
	}{
		{key: vault.Key("a"), value: "source-a"},
		{key: vault.Key("b"), value: "source-b"},
		{key: vault.Key("c"), value: "source-c"},
	} {
		if err := source.Update(ctx, func(tx *vault.Txn) error {
			return sourceRecords.Put(tx, record.key, record.value)
		}); err != nil {
			t.Fatalf("write source record %q: %v", record.key, err)
		}
	}
	if err := destination.Update(ctx, func(tx *vault.Txn) error {
		return destinationRecords.Put(tx, vault.Key("b"), "destination-b")
	}); err != nil {
		t.Fatalf("write existing destination record: %v", err)
	}

	if err := destination.CopyBucketFrom(ctx, source, "records", 2); err != nil {
		t.Fatalf("copy records: %v", err)
	}
	if err := destination.CopyBucketFrom(ctx, source, "records", 2); err != nil {
		t.Fatalf("repeat records copy: %v", err)
	}
	if err := destination.View(ctx, func(tx *vault.Txn) error {
		length, err := destinationRecords.Len(tx)
		if err != nil {
			return err
		}
		if length != 3 {
			t.Fatalf("destination length = %d, want 3", length)
		}
		for _, record := range []struct {
			key  vault.Key
			want string
		}{
			{key: vault.Key("a"), want: "source-a"},
			{key: vault.Key("b"), want: "destination-b"},
			{key: vault.Key("c"), want: "source-c"},
		} {
			got, found, err := destinationRecords.Get(tx, record.key)
			if err != nil || !found || got != record.want {
				t.Fatalf(
					"destination record %q = %q, %t, %v; want %q",
					record.key,
					got,
					found,
					err,
					record.want,
				)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read destination records: %v", err)
	}
	if err := source.View(ctx, func(tx *vault.Txn) error {
		length, err := sourceRecords.Len(tx)
		if err != nil {
			return err
		}
		if length != 3 {
			t.Fatalf("source length after copy = %d, want 3", length)
		}
		return nil
	}); err != nil {
		t.Fatalf("read source records: %v", err)
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
