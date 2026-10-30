package remotecrawl

import (
	"fmt"

	"github.com/D4rk4/yago/yagonode/internal/vault"
)

func readDesiredQueueState(
	tx *vault.Txn,
	collections collections,
) (queueStateSnapshot, error) {
	snapshot := newQueueStateSnapshot()
	length, err := collections.orders.Len(tx)
	if err != nil {
		return queueStateSnapshot{}, fmt.Errorf("read remote crawl queue depth: %w", err)
	}
	if length > MaximumQueueCapacity {
		return queueStateSnapshot{}, fmt.Errorf("remote crawl queue exceeds maximum capacity")
	}
	ordersRead := 0
	if err := collections.orders.Scan(tx, nil, func(
		key vault.Key,
		record queueRecord,
	) (bool, error) {
		ordersRead++
		if ordersRead > MaximumQueueCapacity {
			return false, fmt.Errorf("remote crawl queue exceeds maximum capacity")
		}
		if err := addDesiredQueueRecord(snapshot, key, record); err != nil {
			return false, err
		}

		return true, nil
	}); err != nil {
		return queueStateSnapshot{}, fmt.Errorf("scan remote crawl orders: %w", err)
	}

	return snapshot, nil
}

func addDesiredQueueRecord(
	snapshot queueStateSnapshot,
	key vault.Key,
	record queueRecord,
) error {
	if string(key) != string(sequenceKey(record.Sequence)) {
		return fmt.Errorf("remote crawl order sequence is inconsistent")
	}
	switch record.State {
	case queueStatePending:
		snapshot.pendingRecords[string(key)] = pendingRecord{Sequence: record.Sequence}
	case queueStateLeased:
		return addDesiredLease(snapshot, record)
	default:
		return fmt.Errorf("remote crawl order state %q is unsupported", record.State)
	}

	return nil
}

func addDesiredLease(snapshot queueStateSnapshot, record queueRecord) error {
	if record.Peer == "" || record.LeaseUntil <= 0 {
		return fmt.Errorf("remote crawl leased order is malformed")
	}
	key := string(leaseExpiryKey(record.LeaseUntil, record.Sequence))
	snapshot.leaseExpiries[key] = leaseExpiryRecord{Sequence: record.Sequence}
	snapshot.leasesByPeer[record.Peer]++

	return nil
}

func readActualQueueState(
	tx *vault.Txn,
	collections collections,
) (queueStateSnapshot, error) {
	snapshot := newQueueStateSnapshot()
	if err := scanQueueStateIndex(
		tx, collections.pending, "pending", snapshot.pendingRecords,
	); err != nil {
		return queueStateSnapshot{}, err
	}
	if err := scanQueueStateIndex(
		tx, collections.leaseExpiries, "lease expiry", snapshot.leaseExpiries,
	); err != nil {
		return queueStateSnapshot{}, err
	}
	if err := scanQueueStateIndex(
		tx, collections.leaseCounts, "lease count", snapshot.leasesByPeer,
	); err != nil {
		return queueStateSnapshot{}, err
	}

	return snapshot, nil
}

func scanQueueStateIndex[Record any](
	tx *vault.Txn,
	index *vault.Collection[Record],
	indexName string,
	records map[string]Record,
) error {
	if err := index.Scan(tx, nil, func(key vault.Key, record Record) (bool, error) {
		if len(records) >= MaximumQueueCapacity {
			return false, fmt.Errorf("remote crawl %s index exceeds maximum capacity", indexName)
		}
		records[string(key)] = record

		return true, nil
	}); err != nil {
		return fmt.Errorf("scan remote crawl %s index: %w", indexName, err)
	}

	return nil
}
