# Personalized search

YagoSeek can gently reorder an account's already retrieved results using that
account's recent searches and clicked results. It does not change the eligible
result set, query filters, or ordinary ranking for guests and opted-out
accounts. The feature is disabled by default and requires both the operator's
global setting and an individual account opt-in.

## Enable and invite users

1. In operator settings, set `search.personalization.enabled` to `true`. This
   live setting defaults to `false`.
2. While signed in to the protected operator interface, open
   `/admin/personal-search` and create a one-use invitation. Its lifetime is
   selectable from 1 to 168 hours.
3. Give the invitation token to the intended user. They claim it at
   `/account/claim`, choose an account name and a password of at least 12
   characters, then sign in at `/account/login`.
4. At `/account`, the user explicitly enables “Learn from my searches and
   clicks.”

Account and invitation pages require HTTPS because their host-only cookies are
marked `Secure`. Enrollment is invitation-only; there is no public account
creation endpoint.

An operator can recover an existing account through the admin-authenticated
`POST /api/admin/v1/personal-search/password-reset` endpoint with a JSON body
containing `loginName` and `password`. The password must be at least 12
characters. The request requires a valid operator session and its CSRF token;
the reset invalidates that account's active browser sessions. Never include the
password in URLs, logs, or shell history.

## Personal use and agent API

At `/account`, an owner can inspect recent history, download it as JSON, clear
it, enable or disable learning, and create or revoke account-bound API keys.
Each API key is shown once. The `read` scope supports search; `raw` is optional
and permits the existing raw-content API scope. Send the account key as a bearer
credential to `POST /search`, for example:

```sh
curl -sS https://yago.example/search -H 'Authorization: Bearer ygp_<account-id>_<key-id>_<secret>' -H 'Content-Type: application/json' -d '{"query":"municipal housing policy","search_depth":"advanced"}'
```

For an opted-in account with personalization enabled, that response also has
the private `X-Yago-Impression` header. After selecting a returned result, the
agent can send feedback to the existing `POST /searchclick` endpoint using the
same account key, the exact returned URL, and its one-based position in the
response:

```sh
curl -sS https://yago.example/searchclick -H 'Authorization: Bearer ygp_<account-id>_<key-id>_<secret>' -H 'Content-Type: application/x-www-form-urlencoded' --data-urlencode 't=<X-Yago-Impression value>' --data-urlencode 'i=https://example.org/result' --data-urlencode 'p=1'
```

The token is account-bound, validates the URL and position against the displayed
result set, expires after two hours, and is returned only in a response header
marked `Cache-Control: private, no-store`. It is absent for guests, opted-out
accounts, and searches while the operator switch is off.

When both switches are enabled, browser and agent searches use the same account
profile. Invalid account keys fail closed. Account keys do not grant operator
administration. Revoking a key or disabling the account invalidates it.

## Data, ranking, and privacy

- Successful first-page searches and clicks are recorded only when the account
  opted in and the operator's global switch is on.
- Raw query and click history is retained for at most 90 days and capped at
  1,000 events per account. Owners can export or clear it at any time.
- A short-lived, two-hour impression records displayed URLs and positions so a
  click can be validated against what the user actually saw. Bounded retention
  cleanup removes expired impressions.
- Recent signals decay with a seven-day half-life. Searches are weak signals;
  clicks contribute more, adjusted for displayed rank. The deterministic boost
  is modest and a changed result can explain whether it matched recent topics
  or a previously clicked site.
- Reranking happens after the local/peer/optional-web result blend and before
  stable paging. Account-specific paging windows are isolated from other users.
- The account profile and account ID stay on this node; they are not included in
  peer or web-provider requests. Ordinary search queries continue to follow the
  node's existing local/peer/web search configuration. Aggregate
  search-demand-guided crawling is separate and does not consume personal
  profiles.
- Account-scoped queries are excluded from the shared public autocomplete list
  and redacted from operator full-query logs and activity text; only query
  length/count and ordinary aggregate demand signals remain outside owner
  history.
- Turning off either switch stops new personal event writes and personalization;
  existing history remains available to its owner until cleared or expired.
  Disabling an account removes its history and revokes its credentials.

This is deterministic per-account ranking, not model training or cross-peer
personalization. Relevance feedback, automatic learning of ranking weights, and
cross-peer focused-search routing are out of scope.
