package peerroster_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/boltvault"
	"github.com/D4rk4/yago/yagonode/internal/memvault"
	"github.com/D4rk4/yago/yagonode/internal/peerroster"
	"github.com/D4rk4/yago/yagonode/internal/vault"
)

func openRosterOn(t *testing.T, storage *vault.Vault) peerroster.Roster {
	t.Helper()
	opened, err := peerroster.Open(
		t.Context(),
		storage,
		hashFor("self"),
		func() time.Time { return time.Unix(100, 0) },
		peerroster.Capacity{Reservoir: 8, Active: 4},
	)
	if err != nil {
		t.Fatalf("peerroster.Open: %v", err)
	}

	return opened
}

func TestMigrateLegacyStorageCopiesKnownPeers(t *testing.T) {
	legacy, err := memvault.Open(0)
	if err != nil {
		t.Fatalf("memvault.Open: %v", err)
	}
	t.Cleanup(func() { _ = legacy.Close() })
	peer := seniorSeed(t, "migrated", "203.0.113.7", 8090)
	openRosterOn(t, legacy).Discover(t.Context(), peer)
	dedicated, err := boltvault.OpenWithLockTimeout(
		filepath.Join(t.TempDir(), "peerroster.db"),
		time.Second,
	)
	if err != nil {
		t.Fatalf("open dedicated storage: %v", err)
	}
	t.Cleanup(func() { _ = dedicated.Close() })

	if err := peerroster.MigrateLegacyStorage(t.Context(), legacy, dedicated); err != nil {
		t.Fatalf("MigrateLegacyStorage: %v", err)
	}

	if _, found := openRosterOn(t, dedicated).PeerByHash(t.Context(), peer.Hash); !found {
		t.Fatal("migrated roster lost the known peer")
	}
}

func TestMigrateLegacyStorageRefusesMissingStorage(t *testing.T) {
	err := peerroster.MigrateLegacyStorage(t.Context(), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "migrate legacy peer roster storage") {
		t.Fatalf("migration failure = %v", err)
	}
}
