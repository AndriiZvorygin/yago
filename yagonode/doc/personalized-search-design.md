# Personalized search design

## Scope

Add invite-only end-user accounts, explicit per-account opt-in, and deterministic
profile reranking to the existing Yago search path. Operator authentication,
shared demand crawling, and peer/provider requests remain separate. The global
operator switch and each account's opt-in both default to off.

## Data paths

- Accounts, invitations, browser sessions, account API keys, preferences, search
  events, impressions, and clicks use typed Vault collections in the node's
  existing persistent state. Passwords use Argon2id. Invitation, browser-session,
  CSRF-verifier, and API-key secrets are random and only their hashes are
  persisted.
- Search identity is resolved at the public HTTP boundary from an account
  session cookie or account-owned bearer key. The identity is attached only to
  request context. An opaque account scope separates cached result windows; no
  identity or profile is sent to peers, web providers, or the crawler.
- Profile reranking occurs after the blended local/peer/optional-web retrieval
  pipeline and before the stable paging window. Guests and opted-out accounts
  use the existing ranking path unchanged.
- Raw submitted queries and clicked-result events are retained for 90 days and
  can be listed, exported, or cleared by their owner. Search exposures are
  retained for at most two hours to validate a click against the exact displayed
  URL and position. Account disable revokes sessions and API keys and removes
  personal events and exposures.

## Field mapping

| Input | Stored fields | Use |
|---|---|---|
| Invitation | SHA-256 token verifier, expiry, claimed timestamp | One-use, time-bounded account enrollment |
| Account credentials | normalized login, Argon2id password hash, active flag, opt-in flag | Authentication and explicit learning control |
| Browser/API credential | account ID, SHA-256 session/API-key verifier, expiry or scopes | Resolve the request to one account; show API keys only once |
| Search event | account ID, raw submitted query, event ID, timestamp | Owner-visible history and weak recency/topic signal; terms are normalized only during scoring |
| Impression | account ID, raw query, candidate URL identities, displayed positions, click markers, two-hour expiry | Validate that a later click was actually shown at that position |
| Click event | account ID, query, URL identity, host, displayed rank, timestamp | Stronger source/topic signal, interpreted with exposure and rank |
| Search result | existing title, URL, snippet, source, score, position | Local-only deterministic reranking; candidate set and filters remain unchanged |

## Profile behavior

Use a seven-day half-life and a bounded 90-day event window. Repeated searches
provide a weak interest signal; clicks provide a stronger signal adjusted by
display position and only when the URL was present in that user's impression.
Signals are derived from query/result text and clicked hosts without
topic-specific word lists. Normalize scores to a small bounded boost, preserve
the eligible result set and all query filters, and emit a short explanation for
each boosted result. This is deterministic rule-based personalization, not
per-user model training.

## Recovery and privacy

Search-event writes are best-effort after a successful first-page search and
must not make search unavailable. The per-account event history is bounded to
1,000 events and 90 days; old entries are removed before adding another event
at capacity. Click records are idempotent for a given impression and URL.
Expired impressions, invitations, and sessions are rejected at read time and
removed by bounded cleanup. Account/session secrets are never logged. Existing
aggregate click capture and search-demand crawling remain independent and may
stay disabled without affecting personal signals. A personal profile or account
identifier is never added to peer/provider requests.

## Compatibility and validation

Without the global switch, without an authenticated account, or without account
opt-in, search order and anonymous behavior remain unchanged. Account-scoped
result windows are isolated across users and from anonymous windows. Tests must
cover invitation replay/expiry, credential isolation, opt-in and disable paths,
owner-only history operations, retention, click-impression validation,
position-aware signal decay, result-set/filter preservation, stable paging, and
unchanged guest results.
