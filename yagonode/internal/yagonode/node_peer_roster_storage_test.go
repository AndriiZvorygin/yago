package yagonode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagomodel"
	"github.com/D4rk4/yago/yagonode/internal/peerroster"
	"github.com/D4rk4/yago/yagonode/internal/shardvault"
	"github.com/D4rk4/yago/yagonode/internal/vault"
)

func openSharedTestVault(t *testing.T) *vault.Vault {
	t.Helper()
	shared, err := shardvault.Open(filepath.Join(t.TempDir(), "shared"), 0)
	if err != nil {
		t.Fatalf("open shared vault: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })

	return shared
}

func openTestPeerRosterStorage(t *testing.T, path string, legacy *vault.Vault) peerRosterStorage {
	t.Helper()
	storage, err := openPeerRosterStorage(t.Context(), path, legacy)
	if err != nil {
		t.Fatalf("openPeerRosterStorage: %v", err)
	}

	return storage
}

func openTestRoster(t *testing.T, storage *vault.Vault) peerroster.Roster {
	t.Helper()
	roster, err := openObservedPeerRoster(t.Context(), storage, yagomodel.Hash("LLLLLLLLLLLL"), nil)
	if err != nil {
		t.Fatalf("openObservedPeerRoster: %v", err)
	}

	return roster
}

func rosterTestPeer(t *testing.T, hash string, address string) yagomodel.Seed {
	t.Helper()
	host, err := yagomodel.ParseHost(address)
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}
	peer := networkTestSeed(t)
	peer.Hash = yagomodel.Hash(hash)
	peer.IP = yagomodel.Some(host)

	return peer
}

func rosterKnows(t *testing.T, roster peerroster.Roster, peer yagomodel.Seed) bool {
	t.Helper()
	_, known := roster.PeerByHash(t.Context(), peer.Hash)

	return known
}

func TestPeerRosterStoragePathLivesInDataDirectory(t *testing.T) {
	if got, want := peerRosterStoragePath("/srv/yago"), "/srv/yago/peerroster.db"; got != want {
		t.Fatalf("peerRosterStoragePath = %q, want %q", got, want)
	}
}

func TestOpenPeerRosterStorageWithoutPathUsesSharedVault(t *testing.T) {
	shared := openSharedTestVault(t)
	storage := openTestPeerRosterStorage(t, "", shared)
	if storage.vault != shared || storage.dedicated != nil {
		t.Fatalf("storage = %+v, want shared vault without dedicated file", storage)
	}
	closeVault(storage.dedicated)
	if err := shared.Update(t.Context(), func(*vault.Txn) error { return nil }); err != nil {
		t.Fatalf("shared vault closed with the roster storage: %v", err)
	}
}

func TestOpenPeerRosterStorageStartsFreshInstall(t *testing.T) {
	path := filepath.Join(t.TempDir(), peerRosterStorageFileName)
	storage := openTestPeerRosterStorage(t, path, openSharedTestVault(t))
	t.Cleanup(func() { _ = storage.dedicated.Close() })
	if storage.dedicated == nil || storage.vault != storage.dedicated {
		t.Fatalf("storage = %+v, want dedicated vault", storage)
	}
	roster := openTestRoster(t, storage.vault)
	if roster.KnownPeerCount(t.Context()) != 0 {
		t.Fatal("fresh roster is not empty")
	}
	peer := rosterTestPeer(t, "FRESHPEER000", "203.0.113.1")
	roster.ObserveCaller(t.Context(), peer, yagomodel.PeerSenior)
	if !rosterKnows(t, roster, peer) {
		t.Fatal("fresh dedicated roster did not record the caller")
	}
}

func TestOpenPeerRosterStorageMigratesLegacyRosterOnce(t *testing.T) {
	shared := openSharedTestVault(t)
	migrated := rosterTestPeer(t, "MIGRATED0000", "203.0.113.2")
	legacyRoster := openTestRoster(t, shared)
	legacyRoster.ObserveCaller(t.Context(), migrated, yagomodel.PeerSenior)
	path := filepath.Join(t.TempDir(), peerRosterStorageFileName)

	first := openTestPeerRosterStorage(t, path, shared)
	if !rosterKnows(t, openTestRoster(t, first.vault), migrated) {
		t.Fatal("first start did not migrate the legacy roster")
	}
	if err := first.dedicated.Close(); err != nil {
		t.Fatalf("close first start: %v", err)
	}
	stale := rosterTestPeer(t, "STALELEGACY0", "203.0.113.3")
	legacyRoster.ObserveCaller(t.Context(), stale, yagomodel.PeerSenior)
	if !rosterKnows(t, legacyRoster, stale) {
		t.Fatal("legacy roster did not record the stale caller")
	}

	second := openTestPeerRosterStorage(t, path, shared)
	t.Cleanup(func() { _ = second.dedicated.Close() })
	restarted := openTestRoster(t, second.vault)
	if !rosterKnows(t, restarted, migrated) || rosterKnows(t, restarted, stale) {
		t.Fatalf(
			"second start changed the migrated roster: known=%d",
			restarted.KnownPeerCount(t.Context()),
		)
	}
}

func TestOpenPeerRosterStorageRefusesUnopenablePath(t *testing.T) {
	directory := t.TempDir()
	_, err := openPeerRosterStorage(t.Context(), directory, openSharedTestVault(t))
	if err == nil || !strings.Contains(err.Error(), "open peer roster storage") {
		t.Fatalf("open error = %v, want peer roster storage open failure", err)
	}
}

func TestOpenPeerRosterStorageReleasesFileWhenMigrationFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), peerRosterStorageFileName)
	_, err := openPeerRosterStorage(t.Context(), path, nil)
	if err == nil || !strings.Contains(err.Error(), "migrate legacy peer roster storage") {
		t.Fatalf("migration error = %v, want legacy roster migration failure", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("dedicated file missing after failed migration: %v", statErr)
	}
	reopened := openTestPeerRosterStorage(t, path, openSharedTestVault(t))
	if err := reopened.dedicated.Close(); err != nil {
		t.Fatalf("close reopened storage: %v", err)
	}
}

func holdSharedWriteAdmission(t *testing.T, shared *vault.Vault) {
	t.Helper()
	probe, err := vault.Register(shared, "peerroster_contention_probe", vault.JSONCodec[int]{})
	if err != nil {
		t.Fatalf("register contention probe: %v", err)
	}
	held := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_ = shared.Update(context.Background(), func(tx *vault.Txn) error {
			for index := range 256 {
				if err := probe.Put(
					tx,
					vault.Key(fmt.Sprintf("ingest-%03d", index)),
					index,
				); err != nil {
					return fmt.Errorf("put contention probe: %w", err)
				}
			}
			close(held)
			<-release

			return nil
		})
	}()
	<-held
	t.Cleanup(func() {
		close(release)
		<-finished
	})
}

func TestDedicatedPeerRosterWritesWhileSharedWriteAdmissionIsHeld(t *testing.T) {
	shared := openSharedTestVault(t)
	sharedRoster := openTestRoster(t, shared)
	storage := openTestPeerRosterStorage(
		t,
		filepath.Join(t.TempDir(), peerRosterStorageFileName),
		shared,
	)
	t.Cleanup(func() { _ = storage.dedicated.Close() })
	dedicatedRoster := openTestRoster(t, storage.vault)
	holdSharedWriteAdmission(t, shared)
	peer := rosterTestPeer(t, "CONTENDED000", "203.0.113.4")

	sharedContext, cancelShared := context.WithTimeout(t.Context(), nodePeerLifecycleWriteTimeout)
	defer cancelShared()
	sharedRoster.ObserveCaller(sharedContext, peer, yagomodel.PeerSenior)
	if rosterKnows(t, sharedRoster, peer) {
		t.Fatal("shared-vault roster recorded the caller while ingest held write admission")
	}

	dedicatedContext, cancelDedicated := context.WithTimeout(
		t.Context(),
		nodePeerLifecycleWriteTimeout,
	)
	defer cancelDedicated()
	started := time.Now()
	dedicatedRoster.ObserveCaller(dedicatedContext, peer, yagomodel.PeerSenior)
	if !rosterKnows(t, dedicatedRoster, peer) {
		t.Fatalf("dedicated roster did not record the caller within %s", time.Since(started))
	}
}

func TestAssembleNodeKeepsPeerRosterInDedicatedStorage(t *testing.T) {
	config := testConfig(t)
	config.PeerRosterPath = peerRosterStoragePath(t.TempDir())
	shared := openTestVault(t)
	assembled := assembleTestNode(t, config, shared)
	defer assembled.peerLifecycle.Close()
	if assembled.rosterVault.dedicated == nil || assembled.rosterVault.vault == shared {
		t.Fatalf("roster storage = %+v, want dedicated vault", assembled.rosterVault)
	}
	if err := assembled.rosterVault.dedicated.Close(); err != nil {
		t.Fatalf("close dedicated roster storage: %v", err)
	}
}

func TestAssembleNodeRefusesUnopenablePeerRosterStorage(t *testing.T) {
	config := testConfig(t)
	config.PeerRosterPath = t.TempDir()
	_, err := assembleNode(context.Background(), config, openTestVault(t), nil, nodeTelemetry{})
	if err == nil || !strings.Contains(err.Error(), "open peer roster storage") {
		t.Fatalf("assemble error = %v, want peer roster storage open failure", err)
	}
}
