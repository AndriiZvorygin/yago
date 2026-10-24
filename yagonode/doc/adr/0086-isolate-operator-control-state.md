# 0086. Isolate operator control state from the corpus vault

Date: 2026-10-24

## Status

Accepted

## Context

The node's large sharded vault contains document and URL metadata, YaCy RWI
postings, and several small operator-owned records. Authentication and RWI
backfill status reads can therefore contend with corpus work even though their
state is small. A separate process for RWI or Bleve would change service
lifecycle, deployment, peer identity, and failure semantics; current evidence
supports isolating the small control records first and measuring again.

The control records already exist in the active main vault. Upgrade must retain
administrator credentials, sessions, scoped API keys, and the backfill cursor
without scanning or copying the corpus or postings.

## Decision

Store administrator credentials, sessions, API keys, and the RWI backfill
checkpoint in the separate bbolt database `${YAGO_DATA_DIR}/yago-control.db`.
During startup, copy only those named
buckets from the existing main vault using bounded pages of 64 entries. Copy is
idempotent, does not replace a destination key that already exists, and leaves
the source records untouched. The existing main vault continues to own
corpus documents, URL metadata, RWI postings, peer identity, and all other
records. Bleve remains in its existing directory and process.

Keep the control database in the same data directory and include it in backups.
If an operator rolls back after control records have changed, the old binary
must receive a consistent backup or the current control buckets must be copied
back while the node is stopped. No new dependency or runtime service is added.

## Consequences

Admin and backfill state reads/writes no longer share the main vault's shard
locks. The first startup performs a bounded read of only the small named source
buckets; later startups skip keys already copied. The new control vault is
additive and does not rewrite the index, corpus, RWI postings, or peer identity.
It consumes additional disk space for the small control buckets and requires
backups to include both databases. The retained legacy rows make rollback
data available, but they do not automatically mirror later control-state
mutations.

Tests cover bounded/idempotent bucket copying, destination precedence, legacy
retention, admin authentication state, API keys, sessions, and backfill resume
state. A separate RWI/Bleve process split remains deferred until live resource
and latency measurements show that it is warranted.
