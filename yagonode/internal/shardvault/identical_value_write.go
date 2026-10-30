package shardvault

import (
	"bytes"

	bolt "go.etcd.io/bbolt"

	"github.com/D4rk4/yago/yagonode/internal/vault"
)

func holdsIdenticalValue(bucket *bolt.Bucket, key vault.Key, encoded []byte) bool {
	return bytes.Equal(bucket.Get(key), encoded)
}
