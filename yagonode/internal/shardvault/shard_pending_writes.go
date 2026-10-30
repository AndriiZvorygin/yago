package shardvault

import bolt "go.etcd.io/bbolt"

func holdsPendingWrites(tx *bolt.Tx) bool {
	stats := tx.Stats()

	return stats.GetNodeCount() > 0
}
