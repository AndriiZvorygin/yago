package adminauth

import (
	"context"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/memvault"
	"github.com/D4rk4/yago/yagonode/internal/vault"
)

func openAdminStateVault(t *testing.T) *vault.Vault {
	t.Helper()
	storage, err := memvault.Open(0)
	if err != nil {
		t.Fatalf("open in-memory vault: %v", err)
	}
	t.Cleanup(func() {
		if err := storage.Close(); err != nil {
			t.Errorf("close in-memory vault: %v", err)
		}
	})
	return storage
}

func TestNewWithLegacyStorageMigratesAdminKeysAndSessionsIdempotently(t *testing.T) {
	ctx := context.Background()
	legacy := openAdminStateVault(t)
	legacyCredentials, err := newCredentialStore(legacy)
	if err != nil {
		t.Fatalf("open legacy credentials: %v", err)
	}
	if err := legacyCredentials.setAdmin(ctx, "operator", "original-password"); err != nil {
		t.Fatalf("write legacy credential: %v", err)
	}
	legacySessions, err := newSessionStore(legacy, DefaultSessionTTL, time.Now)
	if err != nil {
		t.Fatalf("open legacy sessions: %v", err)
	}
	oldSession, err := legacySessions.create(ctx, "operator")
	if err != nil {
		t.Fatalf("write legacy session: %v", err)
	}
	legacyKeys, err := newAPIKeyStore(legacy, time.Now)
	if err != nil {
		t.Fatalf("open legacy API keys: %v", err)
	}
	oldKey, err := legacyKeys.create(ctx, "migration test", []Scope{ScopeAdminRead})
	if err != nil {
		t.Fatalf("write legacy API key: %v", err)
	}

	storage := openAdminStateVault(t)
	service, err := NewWithLegacyStorage(storage, legacy, Config{})
	if err != nil {
		t.Fatalf("open control auth state: %v", err)
	}
	if err := service.creds.setAdmin(ctx, "operator", "updated-password"); err != nil {
		t.Fatalf("update destination credential: %v", err)
	}
	if err := migrateLegacyAdminAuthState(ctx, legacy, storage); err != nil {
		t.Fatalf("repeat legacy migration: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy vault: %v", err)
	}

	valid, err := service.creds.verify(ctx, "operator", "updated-password")
	if err != nil || !valid {
		t.Fatalf("verify migrated credential = %t, %v", valid, err)
	}
	valid, err = service.creds.verify(ctx, "operator", "original-password")
	if err != nil || valid {
		t.Fatalf("legacy credential replaced destination value: %t, %v", valid, err)
	}
	key, valid, err := service.apiKeys.authenticate(ctx, oldKey.Key)
	if err != nil || !valid || !key.hasScope(ScopeAdminRead) {
		t.Fatalf("authenticate migrated key = %#v, %t, %v", key, valid, err)
	}
	session, found, err := service.sessions.lookup(ctx, oldSession.Token)
	if err != nil || !found || session.Username != "operator" {
		t.Fatalf("look up migrated session = %#v, %t, %v", session, found, err)
	}
}
