package peerroster

import (
	"context"
	"fmt"

	"github.com/D4rk4/yago/yagonode/internal/vault"
)

const (
	legacyStorageMigrationMarker  vault.Name = "peerrosterstoragemigration"
	legacyStorageMigrationVersion string     = "1"
)

func MigrateLegacyStorage(ctx context.Context, source *vault.Vault, target *vault.Vault) error {
	if err := vault.MigrateRetainedBuckets(
		ctx,
		source,
		target,
		legacyStorageMigrationMarker,
		legacyStorageMigrationVersion,
		[]vault.Name{peersBucket, peerLifecyclesBucket, peerLifecycleCleanupCursorBucket},
	); err != nil {
		return fmt.Errorf("migrate legacy peer roster storage: %w", err)
	}

	return nil
}
