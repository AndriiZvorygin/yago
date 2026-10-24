package adminauth

import (
	"context"
	"fmt"

	"github.com/D4rk4/yago/yagonode/internal/vault"
)

const adminStateCopyPageSize = 64

func migrateLegacyAdminAuthState(
	ctx context.Context,
	legacyStorage *vault.Vault,
	storage *vault.Vault,
) error {
	for _, bucket := range []vault.Name{
		adminCredentialsBucket,
		adminSessionsBucket,
		adminAPIKeysBucket,
	} {
		if err := storage.CopyBucketFrom(
			ctx,
			legacyStorage,
			bucket,
			adminStateCopyPageSize,
		); err != nil {
			return fmt.Errorf("copy admin bucket %s: %w", bucket, err)
		}
	}
	return nil
}
