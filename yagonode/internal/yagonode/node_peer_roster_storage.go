package yagonode

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/boltvault"
	"github.com/D4rk4/yago/yagonode/internal/peerroster"
	"github.com/D4rk4/yago/yagonode/internal/vault"
)

const (
	peerRosterStorageFileName    = "peerroster.db"
	peerRosterStorageOpenTimeout = 5 * time.Second
)

type peerRosterStorage struct {
	vault     *vault.Vault
	dedicated *vault.Vault
}

func peerRosterStoragePath(dataDirectory string) string {
	return filepath.Join(dataDirectory, peerRosterStorageFileName)
}

func openPeerRosterStorage(
	ctx context.Context,
	path string,
	legacy *vault.Vault,
) (peerRosterStorage, error) {
	if path == "" {
		return peerRosterStorage{vault: legacy}, nil
	}
	dedicated, err := boltvault.OpenWithLockTimeout(path, peerRosterStorageOpenTimeout)
	if err != nil {
		return peerRosterStorage{}, fmt.Errorf("open peer roster storage: %w", err)
	}
	if err := peerroster.MigrateLegacyStorage(ctx, legacy, dedicated); err != nil {
		return peerRosterStorage{}, errors.Join(err, dedicated.Close())
	}

	return peerRosterStorage{vault: dedicated, dedicated: dedicated}, nil
}
