# 0085. Randomize deadline-bounded shard retrieval

Date: 2026-10-22

## Status

Accepted for implementation on 2026-10-22.

## Context

The normal local search uses a Bleve alias over multiple shards. A broad query
can exceed the interactive search budget before the alias returns. A fallback
that always visits shards in fixed order repeatedly favors the same early
shards, so partial results can be systematically unrepresentative.

The existing request budget is 1.8 seconds. Increasing it would make interactive
and agent requests wait longer without guaranteeing complete retrieval.

## Decision

Keep the existing full-text alias search as the primary path. For eligible
plain, first-page searches that reach the primary deadline, use its reserved
final 500 milliseconds for progressive local retrieval. Query shards one at a
time in a uniformly shuffled order generated from Go's operating-system-backed
`crypto/rand` source. Attempt each shard no more than once per request.

Keep each shard's candidate work bounded. Return completed results when time
runs out and expose completed/total shard counts plus an explicit completeness
flag. Totals from an incomplete run are lower bounds. If all shards complete
without candidate truncation, report complete coverage even if the primary
alias attempt timed out.

For a progressive request, a shard larger than the candidate window starts at
a cryptographically randomized internal-ID position and inspects at most 256
matching candidates. Report the sampled shard count alongside shard progress;
sampled windows leave coverage incomplete. Exact, non-progressive searches
retain the existing 4,096-candidate per-shard bound and fallback behavior.
Internal IDs can be sparse, so the random position is not a statistical
guarantee of uniform sampling over live matches; the response remains explicitly
partial.

Expose cumulative progress over NDJSON at `/search/stream` and
`/yacysearch.ndjson`, and let the HTML search page consume the YaCy-compatible
stream when browser streaming is available. Keep the ordinary JSON routes and
all existing fields; add completeness and coverage metadata, including sampled
shards.

## Consequences

Per-request random ordering prevents a fixed shard prefix from receiving every
partial-search opportunity. It does not prove that a short sample is unbiased
for relevance, and it does not provide a statistical guarantee that returned
documents represent the entire index. Partial responses must remain labeled as
partial. The normal exact alias path, filters, later pages, index files, and
request deadline remain unchanged. No new dependency or quantum-randomness
service is required.

Go documents `crypto/rand` as a cryptographically secure random source and
provides unbiased bounded integers through `crypto/rand.Int`:
[package documentation](https://pkg.go.dev/crypto/rand). Research on early
termination in document-ordered indexes reports effectiveness bias when
high-scoring results are not uniformly distributed across document IDs:
[Anytime Ranking on Document-Ordered Indexes](https://arxiv.org/abs/2104.08976).
These sources motivate random scheduling and explicit coverage; they do not
establish an effectiveness gain for this implementation. Validate that claim
with production-safe query evaluation before changing ranking policy.

Acceptance requires deterministic tests for injected shard orders, operating
system random range handling, per-shard work bounds, partial and complete
coverage, cancellation, shard errors, endpoint event ordering, API
completeness, and browser rendering in Chromium and Firefox.
