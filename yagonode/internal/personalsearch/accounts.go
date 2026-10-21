package personalsearch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/D4rk4/yago/yagonode/internal/vault"
)

const (
	accountRecordsBucket      vault.Name = "personalsearch_accounts"
	loginNamesBucket          vault.Name = "personalsearch_login_names"
	invitationRecordsBucket   vault.Name = "personalsearch_invitations"
	invitationExpiryBucket    vault.Name = "personalsearch_invitation_expiry"
	browserSessionsBucket     vault.Name = "personalsearch_sessions"
	sessionExpiryBucket       vault.Name = "personalsearch_session_expiry"
	accountAPIKeysBucket      vault.Name = "personalsearch_api_keys"
	maximumAccountRows                   = 10000
	maximumAccountAPIKeys                = 20
	maximumBrowserSessions               = 32
	maximumInvitationLifetime            = 7 * 24 * time.Hour
	maximumPasswordBytes                 = 1024
	minimumPasswordBytes                 = 12
	accountSessionCookie                 = "__Host-yago_account"
)

var (
	ErrInvitationInvalid  = errors.New("invitation is invalid or expired")
	ErrLoginRejected      = errors.New("login rejected")
	ErrAccountUnavailable = errors.New("account unavailable")
	ErrAccountExists      = errors.New("account name is unavailable")
	ErrCredentialInvalid  = errors.New("credential is invalid")
)

type accountRecord struct {
	ID              string    `json:"id"`
	LoginName       string    `json:"loginName"`
	PasswordHash    string    `json:"passwordHash"`
	Active          bool      `json:"active"`
	Personalization bool      `json:"personalization"`
	CreatedAt       time.Time `json:"createdAt"`
}

type invitationRecord struct {
	ExpiresAt time.Time `json:"expiresAt"`
	ClaimedAt time.Time `json:"claimedAt,omitempty"`
}

type browserSessionRecord struct {
	AccountID string    `json:"accountId"`
	CSRFHash  string    `json:"csrfHash"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type accountAPIKeyRecord struct {
	AccountID  string    `json:"accountId"`
	SecretHash string    `json:"secretHash"`
	Label      string    `json:"label"`
	Scopes     []string  `json:"scopes"`
	CreatedAt  time.Time `json:"createdAt"`
	RevokedAt  time.Time `json:"revokedAt,omitempty"`
}

type jsonRecordCodec[T any] struct{}

func wrapPersonalError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("personal search %s: %w", operation, err)
}

func (jsonRecordCodec[T]) Encode(record T) ([]byte, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, wrapPersonalError("encode personal-search record", err)
	}

	return raw, nil
}

func (jsonRecordCodec[T]) Decode(raw []byte) (T, error) {
	var record T
	err := json.Unmarshal(raw, &record)
	if err != nil {
		return record, wrapPersonalError("decode personal-search record", err)
	}

	return record, nil
}

type AccountsConfig struct {
	SessionTTL time.Duration
	Now        func() time.Time
}

type Accounts struct {
	vault            *vault.Vault
	accounts         *vault.Collection[accountRecord]
	loginNames       *vault.Collection[string]
	invitations      *vault.Collection[invitationRecord]
	invitationExpiry *vault.Collection[string]
	sessions         *vault.Collection[browserSessionRecord]
	sessionExpiry    *vault.Collection[string]
	apiKeys          *vault.Collection[accountAPIKeyRecord]
	sessionTTL       time.Duration
	now              func() time.Time
	passwordWork     chan struct{}
}

type Principal struct {
	AccountID       string
	Personalization bool
	APIKey          bool
	CSRFToken       string
	csrfHash        string
}

type IssuedInvitation struct {
	Token     string
	ExpiresAt time.Time
}

type Account struct {
	ID              string
	LoginName       string
	Active          bool
	Personalization bool
	CreatedAt       time.Time
}

type AccountSession struct {
	Token     string
	CSRFToken string
	ExpiresAt time.Time
}

type AccountAPIKey struct {
	ID        string
	Secret    string
	Label     string
	Scopes    []string
	CreatedAt time.Time
}

type AccountAPIKeyView struct {
	ID        string
	Label     string
	Scopes    []string
	CreatedAt time.Time
}

func NewAccounts(storage *vault.Vault, config AccountsConfig) (*Accounts, error) {
	if config.SessionTTL <= 0 {
		config.SessionTTL = 12 * time.Hour
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	accounts, err := vault.Register(storage, accountRecordsBucket, jsonRecordCodec[accountRecord]{})
	if err != nil {
		return nil, fmt.Errorf("register personal accounts: %w", err)
	}
	loginNames, err := vault.Register(storage, loginNamesBucket, jsonRecordCodec[string]{})
	if err != nil {
		return nil, fmt.Errorf("register personal account login names: %w", err)
	}
	invitations, err := vault.Register(
		storage,
		invitationRecordsBucket,
		jsonRecordCodec[invitationRecord]{},
	)
	if err != nil {
		return nil, fmt.Errorf("register personal account invitations: %w", err)
	}
	invitationExpiry, err := vault.Register(
		storage,
		invitationExpiryBucket,
		jsonRecordCodec[string]{},
	)
	if err != nil {
		return nil, fmt.Errorf("register personal account invitation expiry index: %w", err)
	}
	sessions, err := vault.Register(
		storage,
		browserSessionsBucket,
		jsonRecordCodec[browserSessionRecord]{},
	)
	if err != nil {
		return nil, fmt.Errorf("register personal account sessions: %w", err)
	}
	sessionExpiry, err := vault.Register(storage, sessionExpiryBucket, jsonRecordCodec[string]{})
	if err != nil {
		return nil, fmt.Errorf("register personal account session expiry index: %w", err)
	}
	apiKeys, err := vault.Register(
		storage,
		accountAPIKeysBucket,
		jsonRecordCodec[accountAPIKeyRecord]{},
	)
	if err != nil {
		return nil, fmt.Errorf("register personal account API keys: %w", err)
	}

	return &Accounts{
		vault: storage, accounts: accounts, loginNames: loginNames,
		invitations: invitations, invitationExpiry: invitationExpiry,
		sessions: sessions, sessionExpiry: sessionExpiry, apiKeys: apiKeys,
		sessionTTL: config.SessionTTL, now: config.Now,
		passwordWork: make(chan struct{}, 2),
	}, nil
}

func (a *Accounts) IssueInvitation(
	ctx context.Context,
	ttl time.Duration,
) (IssuedInvitation, error) {
	if ttl <= 0 || ttl > maximumInvitationLifetime {
		ttl = 24 * time.Hour
	}
	token, err := randomSecret(32)
	if err != nil {
		return IssuedInvitation{}, wrapPersonalError("generate invitation", err)
	}
	expires := a.now().Add(ttl)
	err = a.vault.Update(ctx, func(tx *vault.Txn) error {
		length, err := a.invitations.Len(tx)
		if err != nil {
			return wrapPersonalError("count invitations", err)
		}
		if length >= maximumAccountRows {
			return errors.New("invitation capacity reached")
		}
		hash := secretHash(token)
		if err := a.invitations.Put(
			tx,
			vault.Key(hash),
			invitationRecord{ExpiresAt: expires},
		); err != nil {
			return wrapPersonalError("store invitation", err)
		}
		if err := a.invitationExpiry.Put(
			tx,
			vault.Key(accountExpiryKey(expires, hash)),
			hash,
		); err != nil {
			return wrapPersonalError("index invitation expiry", err)
		}
		return nil
	})
	if err != nil {
		return IssuedInvitation{}, fmt.Errorf("issue personal search invitation: %w", err)
	}

	return IssuedInvitation{Token: token, ExpiresAt: expires}, nil
}

func (a *Accounts) ClaimInvitation(
	ctx context.Context,
	token, loginName, password string,
) (Account, error) {
	loginName = normalizedLoginName(loginName)
	if !validInvitationClaim(token, loginName, password) {
		return Account{}, ErrCredentialInvalid
	}
	if !a.acquirePasswordWork(ctx) {
		return Account{}, wrapPersonalError("wait to hash account password", ctx.Err())
	}
	hash, err := passwordHash(password)
	a.releasePasswordWork()
	if err != nil {
		return Account{}, fmt.Errorf("hash account password: %w", err)
	}
	account := accountRecord{
		ID: randomIdentifier(), LoginName: loginName, PasswordHash: hash,
		Active: true, CreatedAt: a.now(),
	}
	if account.ID == "" {
		return Account{}, errors.New("create account identity: random source unavailable")
	}
	key := secretHash(token)
	err = a.vault.Update(ctx, func(tx *vault.Txn) error {
		return a.claimInvitationInTransaction(tx, key, loginName, account)
	})
	if err != nil {
		return Account{}, wrapPersonalError("claim invitation", err)
	}

	return publicAccount(account), nil
}

func validInvitationClaim(token, loginName, password string) bool {
	return len(token) <= 256 && validLoginName(loginName) &&
		len(password) >= minimumPasswordBytes && len(password) <= maximumPasswordBytes
}

func (a *Accounts) claimInvitationInTransaction(
	tx *vault.Txn,
	key, loginName string,
	account accountRecord,
) error {
	invite, found, err := a.invitations.Get(tx, vault.Key(key))
	if err != nil {
		return wrapPersonalError("read invitation", err)
	}
	if !found || !invite.ClaimedAt.IsZero() || !invite.ExpiresAt.After(a.now()) {
		return ErrInvitationInvalid
	}
	if _, found, err := a.loginNames.Get(tx, vault.Key(loginName)); err != nil {
		return wrapPersonalError("check account name", err)
	} else if found {
		return ErrAccountExists
	}
	count, err := a.accounts.Len(tx)
	if err != nil {
		return wrapPersonalError("count accounts", err)
	}
	if count >= maximumAccountRows {
		return errors.New("account capacity reached")
	}
	if err := a.accounts.Put(tx, vault.Key(account.ID), account); err != nil {
		return wrapPersonalError("store account", err)
	}
	if err := a.loginNames.Put(tx, vault.Key(loginName), account.ID); err != nil {
		return wrapPersonalError("store account login name", err)
	}
	invite.ClaimedAt = a.now()
	if err := a.invitations.Put(tx, vault.Key(key), invite); err != nil {
		return wrapPersonalError("mark invitation claimed", err)
	}

	return nil
}

func (a *Accounts) Login(
	ctx context.Context,
	loginName, password string,
) (AccountSession, Account, error) {
	if !a.acquirePasswordWork(ctx) {
		return AccountSession{}, Account{}, wrapPersonalError(
			"wait to verify account password",
			ctx.Err(),
		)
	}
	defer a.releasePasswordWork()
	var record accountRecord
	found := false
	err := a.vault.View(ctx, func(tx *vault.Txn) error {
		id, exists, err := a.loginNames.Get(tx, vault.Key(normalizedLoginName(loginName)))
		if err != nil || !exists {
			return wrapPersonalError("read account login name", err)
		}
		record, found, err = a.accounts.Get(tx, vault.Key(id))
		return wrapPersonalError("read account credentials", err)
	})
	if err != nil {
		return AccountSession{}, Account{}, fmt.Errorf("read personal account: %w", err)
	}
	encodedHash := accountDummyPasswordHash
	if found {
		encodedHash = record.PasswordHash
	}
	tooLong := len(password) > maximumPasswordBytes
	if tooLong {
		password = password[:maximumPasswordBytes]
	}
	valid, err := verifyAccountPassword(encodedHash, password)
	if err != nil {
		return AccountSession{}, Account{}, fmt.Errorf("verify account password: %w", err)
	}
	if !found || !record.Active || !valid || tooLong {
		return AccountSession{}, Account{}, ErrLoginRejected
	}
	session, err := a.createSession(ctx, record.ID)
	if err != nil {
		return AccountSession{}, Account{}, err
	}

	return session, publicAccount(record), nil
}

// ResetPassword changes an active account's password and invalidates its
// existing browser sessions in the same durable transaction.
func (a *Accounts) ResetPassword(ctx context.Context, loginName, password string) error {
	loginName = normalizedLoginName(loginName)
	if !validLoginName(loginName) || len(password) < minimumPasswordBytes ||
		len(password) > maximumPasswordBytes {
		return ErrCredentialInvalid
	}
	if !a.acquirePasswordWork(ctx) {
		return wrapPersonalError("wait to hash reset account password", ctx.Err())
	}
	hash, err := passwordHash(password)
	a.releasePasswordWork()
	if err != nil {
		return fmt.Errorf("hash reset account password: %w", err)
	}
	if err := a.resetPasswordHash(ctx, loginName, hash); err != nil {
		return fmt.Errorf("reset personal account password: %w", err)
	}

	return nil
}

func (a *Accounts) resetPasswordHash(ctx context.Context, loginName, hash string) error {
	if err := a.vault.Update(ctx, func(tx *vault.Txn) error {
		return a.resetPasswordInTransaction(tx, loginName, hash)
	}); err != nil {
		return fmt.Errorf("persist password reset: %w", err)
	}

	return nil
}

func (a *Accounts) resetPasswordInTransaction(
	tx *vault.Txn,
	loginName, hash string,
) error {
	accountID, found, err := a.loginNames.Get(tx, vault.Key(loginName))
	if err != nil {
		return wrapPersonalError("find account for password reset", err)
	}
	if !found {
		return ErrAccountUnavailable
	}
	record, found, err := a.accounts.Get(tx, vault.Key(accountID))
	if err != nil {
		return wrapPersonalError("read account for password reset", err)
	}
	if !found || !record.Active {
		return ErrAccountUnavailable
	}
	record.PasswordHash = hash
	if err := a.accounts.Put(tx, vault.Key(accountID), record); err != nil {
		return wrapPersonalError("store reset account password", err)
	}

	return a.revokeAccountSessionsInTransaction(tx, accountID)
}

func (a *Accounts) createSession(ctx context.Context, accountID string) (AccountSession, error) {
	secret, err := randomSecret(32)
	if err != nil {
		return AccountSession{}, wrapPersonalError("generate account session", err)
	}
	csrf, err := randomSecret(24)
	if err != nil {
		return AccountSession{}, wrapPersonalError("generate account CSRF token", err)
	}
	expires := a.now().Add(a.sessionTTL)
	record := browserSessionRecord{
		AccountID: accountID,
		CSRFHash:  secretHash(csrf),
		ExpiresAt: expires,
	}
	sessionKey := accountID + ":" + secretHash(secret)
	if err := a.vault.Update(ctx, func(tx *vault.Txn) error {
		active, err := a.removeExpiredSessions(tx, accountID)
		if err != nil {
			return err
		}
		if active >= maximumBrowserSessions {
			return errors.New("account session limit reached")
		}
		if err := a.sessions.Put(tx, vault.Key(sessionKey), record); err != nil {
			return wrapPersonalError("store account session", err)
		}
		if err := a.sessionExpiry.Put(
			tx,
			vault.Key(accountExpiryKey(expires, sessionKey)),
			sessionKey,
		); err != nil {
			return wrapPersonalError("index account session expiry", err)
		}
		return nil
	}); err != nil {
		return AccountSession{}, fmt.Errorf("create personal account session: %w", err)
	}

	return AccountSession{Token: accountID + "." + secret, CSRFToken: csrf, ExpiresAt: expires}, nil
}

func (a *Accounts) removeExpiredSessions(tx *vault.Txn, accountID string) (int, error) {
	var active int
	var expired []vault.Key
	if err := a.sessions.Scan(
		tx,
		vault.Key(accountID+":"),
		func(key vault.Key, stored browserSessionRecord) (bool, error) {
			if !stored.ExpiresAt.After(a.now()) {
				expired = append(expired, key)
				return true, nil
			}
			active++
			return active < maximumBrowserSessions, nil
		},
	); err != nil {
		return 0, wrapPersonalError("scan account sessions", err)
	}
	for _, key := range expired {
		stored, found, err := a.sessions.Get(tx, key)
		if err != nil {
			return 0, wrapPersonalError("read expired account session", err)
		}
		if !found {
			continue
		}
		if _, err := a.sessionExpiry.Delete(
			tx,
			vault.Key(accountExpiryKey(stored.ExpiresAt, string(key))),
		); err != nil {
			return 0, wrapPersonalError("remove account session expiry", err)
		}
		if _, err := a.sessions.Delete(tx, key); err != nil {
			return 0, wrapPersonalError("remove expired account session", err)
		}
	}

	return active, nil
}

func (a *Accounts) AuthenticateSession(ctx context.Context, token string) (Principal, bool, error) {
	if token == "" || len(token) > 256 {
		return Principal{}, false, nil
	}
	accountID, secret, validToken := strings.Cut(token, ".")
	if !validToken || accountID == "" || secret == "" {
		return Principal{}, false, nil
	}
	var session browserSessionRecord
	found := false
	err := a.vault.View(ctx, func(tx *vault.Txn) error {
		var err error
		session, found, err = a.sessions.Get(tx, vault.Key(accountID+":"+secretHash(secret)))
		return wrapPersonalError("read account session", err)
	})
	if err != nil {
		return Principal{}, false, fmt.Errorf("read personal session: %w", err)
	}
	if !found || !session.ExpiresAt.After(a.now()) {
		return Principal{}, false, nil
	}
	account, ok, err := a.AccountByID(ctx, session.AccountID)
	if err != nil || !ok || !account.Active {
		return Principal{}, false, err
	}

	return Principal{
		AccountID: account.ID, Personalization: account.Personalization,
		csrfHash: session.CSRFHash,
	}, true, nil
}

func (a *Accounts) Logout(ctx context.Context, token string) error {
	accountID, secret, validToken := strings.Cut(token, ".")
	if !validToken || accountID == "" || secret == "" {
		return nil
	}
	if err := a.vault.Update(ctx, func(tx *vault.Txn) error {
		key := vault.Key(accountID + ":" + secretHash(secret))
		record, found, err := a.sessions.Get(tx, key)
		if err != nil || !found {
			return wrapPersonalError("read account session for logout", err)
		}
		if _, err := a.sessionExpiry.Delete(
			tx,
			vault.Key(accountExpiryKey(record.ExpiresAt, string(key))),
		); err != nil {
			return wrapPersonalError("remove account session expiry", err)
		}
		_, err = a.sessions.Delete(tx, key)
		return wrapPersonalError("remove account session", err)
	}); err != nil {
		return fmt.Errorf("delete personal account session: %w", err)
	}

	return nil
}

func (a *Accounts) AccountByID(ctx context.Context, id string) (Account, bool, error) {
	var record accountRecord
	var found bool
	err := a.vault.View(ctx, func(tx *vault.Txn) error {
		var err error
		record, found, err = a.accounts.Get(tx, vault.Key(id))
		return wrapPersonalError("read account details", err)
	})
	if err != nil {
		return Account{}, false, fmt.Errorf("read personal account details: %w", err)
	}

	return publicAccount(record), found, nil
}

func (a *Accounts) SetPersonalization(ctx context.Context, accountID string, enabled bool) error {
	if err := a.vault.Update(ctx, func(tx *vault.Txn) error {
		record, found, err := a.accounts.Get(tx, vault.Key(accountID))
		if err != nil {
			return wrapPersonalError("read account preference", err)
		}
		if !found || !record.Active {
			return ErrAccountUnavailable
		}
		record.Personalization = enabled
		return a.accounts.Put(tx, vault.Key(accountID), record)
	}); err != nil {
		return fmt.Errorf("update personal search preference: %w", err)
	}

	return nil
}

func (a *Accounts) DisableAccount(ctx context.Context, accountID string) error {
	if err := a.vault.Update(ctx, func(tx *vault.Txn) error {
		record, found, err := a.accounts.Get(tx, vault.Key(accountID))
		if err != nil {
			return wrapPersonalError("read account to disable", err)
		}
		if !found {
			return ErrAccountUnavailable
		}
		record.Active = false
		record.Personalization = false
		if err := a.accounts.Put(tx, vault.Key(accountID), record); err != nil {
			return wrapPersonalError("disable account", err)
		}
		_, err = a.loginNames.Delete(tx, vault.Key(record.LoginName))
		return wrapPersonalError("remove disabled account login name", err)
	}); err != nil {
		return fmt.Errorf("disable personal account: %w", err)
	}
	if err := a.revokeAccountSessions(ctx, accountID); err != nil {
		return wrapPersonalError("revoke disabled account sessions", err)
	}
	return a.revokeAccountAPIKeys(ctx, accountID)
}

func (a *Accounts) CreateAPIKey(
	ctx context.Context,
	accountID, label string,
	scopes []string,
) (AccountAPIKey, error) {
	label = strings.TrimSpace(label)
	if len(label) > 80 {
		return AccountAPIKey{}, ErrCredentialInvalid
	}
	scopes, err := normalizedAPIScopes(scopes)
	if err != nil {
		return AccountAPIKey{}, wrapPersonalError("validate API key scopes", err)
	}
	id, err := randomHex(9)
	if err != nil {
		return AccountAPIKey{}, wrapPersonalError("generate API key identifier", err)
	}
	secret, err := randomHex(32)
	if err != nil {
		return AccountAPIKey{}, wrapPersonalError("generate API key secret", err)
	}
	key := "ygp_" + accountID + "_" + id + "_" + secret
	record := accountAPIKeyRecord{
		AccountID: accountID, SecretHash: secretHash(secret), Label: label,
		Scopes: scopes, CreatedAt: a.now(),
	}
	storageKey := accountID + ":" + id
	err = a.vault.Update(ctx, func(tx *vault.Txn) error {
		account, found, err := a.accounts.Get(tx, vault.Key(accountID))
		if err != nil {
			return wrapPersonalError("read account for API key", err)
		}
		if !found || !account.Active {
			return ErrAccountUnavailable
		}
		count := 0
		if err := a.apiKeys.Scan(
			tx,
			vault.Key(accountID+":"),
			func(_ vault.Key, row accountAPIKeyRecord) (bool, error) {
				if row.RevokedAt.IsZero() {
					count++
				}
				return true, nil
			},
		); err != nil {
			return wrapPersonalError("count account API keys", err)
		}
		if count >= maximumAccountAPIKeys {
			return errors.New("account API key limit reached")
		}
		if err := a.apiKeys.Put(tx, vault.Key(storageKey), record); err != nil {
			return wrapPersonalError("store account API key", err)
		}
		return nil
	})
	if err != nil {
		return AccountAPIKey{}, fmt.Errorf("create personal search API key: %w", err)
	}

	return AccountAPIKey{
		ID:        id,
		Secret:    key,
		Label:     label,
		Scopes:    scopes,
		CreatedAt: record.CreatedAt,
	}, nil
}

func (a *Accounts) AuthenticateAPIKey(
	ctx context.Context,
	token string,
	required string,
) (Principal, bool, error) {
	principal, known, allowed, err := a.AuthorizeAPIKey(ctx, token, required)
	return principal, known && allowed, err
}

func (a *Accounts) AuthorizeAPIKey(
	ctx context.Context,
	token string,
	required string,
) (Principal, bool, bool, error) {
	parts := strings.Split(token, "_")
	if len(parts) != 4 || parts[0] != "ygp" || parts[1] == "" || parts[2] == "" || parts[3] == "" ||
		len(token) > 256 {
		return Principal{}, false, false, nil
	}
	var key accountAPIKeyRecord
	found := false
	err := a.vault.View(ctx, func(tx *vault.Txn) error {
		var err error
		key, found, err = a.apiKeys.Get(tx, vault.Key(parts[1]+":"+parts[2]))
		return wrapPersonalError("read account API key", err)
	})
	if err != nil {
		return Principal{}, false, false, fmt.Errorf("read personal API key: %w", err)
	}
	validSecret := subtle.ConstantTimeCompare(
		[]byte(secretHash(parts[3])),
		[]byte(key.SecretHash),
	) == 1
	if !found || !key.RevokedAt.IsZero() || !validSecret {
		return Principal{}, false, false, nil
	}
	account, found, err := a.AccountByID(ctx, key.AccountID)
	if err != nil || !found || !account.Active {
		return Principal{}, false, false, wrapPersonalError("read API key account", err)
	}
	principal := Principal{
		AccountID:       account.ID,
		Personalization: account.Personalization,
		APIKey:          true,
	}
	return principal, true, containsScope(key.Scopes, required), nil
}

func (a *Accounts) ListAPIKeys(ctx context.Context, accountID string) ([]AccountAPIKeyView, error) {
	keys := make([]AccountAPIKeyView, 0)
	err := a.vault.View(ctx, func(tx *vault.Txn) error {
		return a.apiKeys.Scan(
			tx,
			vault.Key(accountID+":"),
			func(key vault.Key, record accountAPIKeyRecord) (bool, error) {
				if record.RevokedAt.IsZero() {
					keys = append(keys, AccountAPIKeyView{
						ID:    strings.TrimPrefix(string(key), accountID+":"),
						Label: record.Label,
						Scopes: append(
							[]string(nil),
							record.Scopes...),
						CreatedAt: record.CreatedAt,
					})
				}
				return true, nil
			},
		)
	})
	if err != nil {
		return nil, fmt.Errorf("list personal search API keys: %w", err)
	}

	return keys, nil
}

func (a *Accounts) RevokeAPIKey(ctx context.Context, accountID, id string) error {
	key := vault.Key(accountID + ":" + id)
	if err := a.vault.Update(ctx, func(tx *vault.Txn) error {
		record, found, err := a.apiKeys.Get(tx, key)
		if err != nil {
			return wrapPersonalError("read API key to revoke", err)
		}
		if !found || !record.RevokedAt.IsZero() {
			return ErrCredentialInvalid
		}
		_, err = a.apiKeys.Delete(tx, key)
		return wrapPersonalError("delete account API key", err)
	}); err != nil {
		return fmt.Errorf("revoke personal search API key: %w", err)
	}

	return nil
}

func (a *Accounts) revokeAccountSessions(ctx context.Context, accountID string) error {
	if err := a.vault.Update(ctx, func(tx *vault.Txn) error {
		return a.revokeAccountSessionsInTransaction(tx, accountID)
	}); err != nil {
		return fmt.Errorf("revoke personal account sessions: %w", err)
	}

	return nil
}

func (a *Accounts) revokeAccountSessionsInTransaction(
	tx *vault.Txn,
	accountID string,
) error {
	keys, err := a.accountSessionKeys(tx, accountID)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := a.revokeAccountSessionInTransaction(tx, key); err != nil {
			return err
		}
	}

	return nil
}

func (a *Accounts) accountSessionKeys(tx *vault.Txn, accountID string) ([]vault.Key, error) {
	var keys []vault.Key
	err := a.sessions.Scan(
		tx,
		vault.Key(accountID+":"),
		func(key vault.Key, _ browserSessionRecord) (bool, error) {
			keys = append(keys, key)
			return true, nil
		},
	)
	if err != nil {
		return nil, wrapPersonalError("scan account sessions to revoke", err)
	}

	return keys, nil
}

func (a *Accounts) revokeAccountSessionInTransaction(tx *vault.Txn, key vault.Key) error {
	record, found, err := a.sessions.Get(tx, key)
	if err != nil {
		return wrapPersonalError("read account session to revoke", err)
	}
	if found {
		if _, err := a.sessionExpiry.Delete(
			tx,
			vault.Key(accountExpiryKey(record.ExpiresAt, string(key))),
		); err != nil {
			return wrapPersonalError("remove account session expiry", err)
		}
	}
	if _, err := a.sessions.Delete(tx, key); err != nil {
		return wrapPersonalError("delete account session", err)
	}

	return nil
}

func (a *Accounts) PruneExpiredSessions(ctx context.Context, batch int) (int, error) {
	return a.pruneExpiredCredentialRows(ctx, batch, a.sessionExpiry, a.sessions)
}

func (a *Accounts) PruneExpiredInvitations(ctx context.Context, batch int) (int, error) {
	return a.pruneExpiredCredentialRows(ctx, batch, a.invitationExpiry, a.invitations)
}

func (a *Accounts) pruneExpiredCredentialRows(
	ctx context.Context,
	batch int,
	expiry *vault.Collection[string],
	rows interface {
		Delete(*vault.Txn, vault.Key) (bool, error)
	},
) (int, error) {
	if batch < 1 {
		batch = 250
	}
	cutoff := a.now().UnixNano()
	type expiryRow struct {
		key string
		row string
	}
	var expired []expiryRow
	if err := a.vault.View(ctx, func(tx *vault.Txn) error {
		return expiry.Scan(tx, nil, func(key vault.Key, row string) (bool, error) {
			timestamp, valid := timestampFromExpiryKey(key)
			if !valid || timestamp >= cutoff || len(expired) >= batch {
				return false, nil
			}
			expired = append(expired, expiryRow{key: string(key), row: row})
			return true, nil
		})
	}); err != nil {
		return 0, fmt.Errorf("scan expired account credential rows: %w", err)
	}
	removed := 0
	if len(expired) == 0 {
		return 0, nil
	}
	if err := a.vault.Update(ctx, func(tx *vault.Txn) error {
		for _, item := range expired {
			if _, err := rows.Delete(tx, vault.Key(item.row)); err != nil {
				return wrapPersonalError("delete expired credential", err)
			}
			if _, err := expiry.Delete(tx, vault.Key(item.key)); err != nil {
				return wrapPersonalError("delete credential expiry", err)
			}
			removed++
		}
		return nil
	}); err != nil {
		return 0, fmt.Errorf("prune expired account credential rows: %w", err)
	}

	return removed, nil
}

func (a *Accounts) RunRetention(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		_, _ = a.PruneExpiredSessions(ctx, 500)
		_, _ = a.PruneExpiredInvitations(ctx, 500)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func accountExpiryKey(expires time.Time, key string) string {
	return personalExpiryKeyTime(expires, key)
}

func (a *Accounts) revokeAccountAPIKeys(ctx context.Context, accountID string) error {
	if err := a.vault.Update(ctx, func(tx *vault.Txn) error {
		var keys []vault.Key
		if err := a.apiKeys.Scan(
			tx,
			vault.Key(accountID+":"),
			func(key vault.Key, _ accountAPIKeyRecord) (bool, error) {
				keys = append(keys, key)
				return true, nil
			},
		); err != nil {
			return wrapPersonalError("scan API keys to revoke", err)
		}
		for _, key := range keys {
			if _, err := a.apiKeys.Delete(tx, key); err != nil {
				return wrapPersonalError("delete revoked account API key", err)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("revoke personal account API keys: %w", err)
	}

	return nil
}

func (a *Accounts) acquirePasswordWork(ctx context.Context) bool {
	select {
	case a.passwordWork <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (a *Accounts) releasePasswordWork() {
	<-a.passwordWork
}

func publicAccount(record accountRecord) Account {
	return Account{
		ID: record.ID, LoginName: record.LoginName, Active: record.Active,
		Personalization: record.Personalization, CreatedAt: record.CreatedAt,
	}
}

func normalizedLoginName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validLoginName(value string) bool {
	if len(value) < 3 || len(value) > 40 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

func randomSecret(bytes int) (string, error) {
	secret := make([]byte, bytes)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(secret), nil
}

func randomIdentifier() string {
	identifier, err := randomHex(12)
	if err != nil {
		return ""
	}
	return identifier
}

func randomHex(length int) (string, error) {
	secret := make([]byte, length)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate credential: %w", err)
	}
	return hex.EncodeToString(secret), nil
}

func secretHash(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(digest[:])
}

func passwordHash(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", wrapPersonalError("generate password salt", err)
	}
	key := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return "$argon2id$v=19$m=65536,t=3,p=2$" + base64.RawStdEncoding.EncodeToString(
		salt,
	) + "$" + base64.RawStdEncoding.EncodeToString(
		key,
	), nil
}

func verifyAccountPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" ||
		parts[3] != "m=65536,t=3,p=2" {
		return false, errors.New("stored password verifier is malformed")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		return false, errors.New("stored password salt is malformed")
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) != 32 {
		return false, errors.New("stored password verifier is malformed")
	}
	actual := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

var accountDummyPasswordHash = mustAccountDummyPasswordHash()

func mustAccountDummyPasswordHash() string {
	hash, err := passwordHash("personal-search-unknown-account-dummy")
	if err != nil {
		panic(err)
	}
	return hash
}

func normalizedAPIScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return []string{"read"}, nil
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.ToLower(strings.TrimSpace(scope))
		if scope != "read" && scope != "raw" {
			return nil, ErrCredentialInvalid
		}
		if !seen[scope] {
			seen[scope] = true
			result = append(result, scope)
		}
	}
	if !seen["read"] {
		return nil, ErrCredentialInvalid
	}

	return result, nil
}

func containsScope(scopes []string, required string) bool {
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}
