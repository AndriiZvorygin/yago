package remotecrawl

import (
	"fmt"
	"maps"
	"testing"

	"github.com/D4rk4/yago/yagonode/internal/vault"
)

func TestRemoteCrawlActualQueueStateReadsEveryIndexEntry(t *testing.T) {
	registered, storage, _ := registerRemoteCrawlFaultCollections(t)
	wantPending := map[string]pendingRecord{
		string(sequenceKey(1)): {Sequence: 1},
		string(sequenceKey(2)): {Sequence: 2},
	}
	wantExpiries := map[string]leaseExpiryRecord{
		string(leaseExpiryKey(10, 3)): {Sequence: 3},
		string(leaseExpiryKey(20, 4)): {Sequence: 4},
	}
	wantLeasesByPeer := map[string]uint64{"peer-a": 1, "peer-b": 2}
	if err := storage.Update(t.Context(), func(tx *vault.Txn) error {
		for key, record := range wantPending {
			if err := registered.pending.Put(tx, vault.Key(key), record); err != nil {
				return fmt.Errorf("store pending index fixture: %w", err)
			}
		}
		for key, record := range wantExpiries {
			if err := registered.leaseExpiries.Put(tx, vault.Key(key), record); err != nil {
				return fmt.Errorf("store lease expiry index fixture: %w", err)
			}
		}
		for peer, total := range wantLeasesByPeer {
			if err := registered.leaseCounts.Put(tx, vault.Key(peer), total); err != nil {
				return fmt.Errorf("store lease count index fixture: %w", err)
			}
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var actual queueStateSnapshot
	if err := storage.View(t.Context(), func(tx *vault.Txn) error {
		var err error
		actual, err = readActualQueueState(tx, registered)

		return err
	}); err != nil {
		t.Fatal(err)
	}

	if !maps.Equal(actual.pendingRecords, wantPending) {
		t.Fatalf("pending index = %v, want %v", actual.pendingRecords, wantPending)
	}
	if !maps.Equal(actual.leaseExpiries, wantExpiries) {
		t.Fatalf("lease expiry index = %v, want %v", actual.leaseExpiries, wantExpiries)
	}
	if !maps.Equal(actual.leasesByPeer, wantLeasesByPeer) {
		t.Fatalf("lease count index = %v, want %v", actual.leasesByPeer, wantLeasesByPeer)
	}
}
