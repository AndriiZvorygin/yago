# YaCy document migration

`yacy-migrate` provides a bounded, resumable first step for importing indexed
YaCy documents into a Yago node. It uses YaCy's read-only Solr `select` cursor
API and Yago's authenticated `POST /admin/index/import` route. It does not read
or write either engine's on-disk index files.

This is a document-content migration, not a complete YaCy crawl-state or peer
index migration. The inspected YaCy interfaces do not export the full URLDB,
pending frontier, focused-crawler sidecar, or exact per-URL next-visit schedule.
The adapter preserves source values that are present in Solr and does not infer
missing state. Imported documents are locally stored and searchable through
Yago's normal document/index owners, but this adapter does not generate peer
RWI postings. Do not cut over or stop YaCy based only on a successful import.
See [the design and field-gap note](yacy-migration-design.md).

## Export

Build/run from the `yagonode` module. Use a local YaCy endpoint or a protected
TLS tunnel; Basic authentication over an untrusted network is not safe. Keep
the output and checkpoint in private storage because documents may contain
non-public URLs or text.

```sh
cd yagonode
YACY_USERNAME='admin-user' YACY_PASSWORD='read-only-password' \
  go run ./cmd/yacy-migrate export \
    --solr-url 'http://127.0.0.1:8090/solr/select?core=collection1' \
    --source-id 'local-yacy-export-2026-10' \
    --output '/secure/private/yacy-documents.jsonl' \
    --rows 16
```

The endpoint must be a read-only Solr `select` URL. `--rows` is limited to
1–64. The default is 16. `--dry-run` asks Solr for the current row count without
creating an output or checkpoint:

```sh
cd yagonode
go run ./cmd/yacy-migrate export \
  --solr-url 'http://127.0.0.1:8090/solr/select?core=collection1' \
  --source-id 'local-yacy-export-2026-10' --dry-run
```

An export writes a versioned JSONL header and records. Each committed Solr
cursor page is synced before the checkpoint advances. On restart, rerun the
same command with the same source ID and paths; only bytes after the last
committed output offset are discarded. The cursor is not a point-in-time
snapshot: writes or deletes during export can make one pass inconsistent. Run
a fresh export/reconciliation pass and compare counts before any migration
cutover. Use a new output path for each reconciliation pass.

## Import

The Yago destination must be running with its crawler/ingest runtime enabled.
Create a bearer API key scoped to `admin:write`, then import over a trusted
network or TLS:

```sh
cd yagonode
YAGO_API_KEY='admin-write-key' \
  go run ./cmd/yacy-migrate import \
    --input '/secure/private/yacy-documents.jsonl' \
    --endpoint 'https://yago.example/admin/index/import'
```

Validate without contacting Yago or writing an import checkpoint:

```sh
cd yagonode
go run ./cmd/yacy-migrate import \
  --input '/secure/private/yacy-documents.jsonl' \
  --endpoint 'https://yago.example/admin/index/import' --dry-run
```

The importer holds a bounded batch (at most 64 records and below the existing
ingest request limit), waits for normal ingest backpressure, and checkpoints
only after it receives a valid receipt for every record. On interruption,
rerun with the same input and endpoint. The default checkpoint is
`<input>.import.state.json`; `--state` selects another path. Keep the input
unchanged. A changed size or modification time is rejected rather than
resumed against stale progress.

Replaying a batch is idempotent: the normalized/canonical URL and content hash
are checked before ingest. Matching documents return `already-present`; a
different destination document is reported as `conflict` and is not replaced.
The importer preserves separate URLs even when their text hashes match, so
aliases and link evidence are not discarded. Source rows without a supported
required field or beyond Yago's existing document limits stop the pass with an
error; the tool does not truncate or silently skip them.

YaCy's current Solr export has no exact next-visit schedule. A versioned JSONL
record may carry `nextVisitAt` when an operator has a separately verified
source for it. Only such an explicit value creates a durable Yago recrawl
schedule, and only a due schedule enters the recrawl frontier. Other imported
records are not added to new-crawl work.

## Field mapping and known gaps

The exact stored-field projection, mapping, size limits, source paths, and
missing crawl-state details are recorded in
[`yacy-migration-design.md`](yacy-migration-design.md). In particular, the
available export does not provide full aliases, URLDB status, pending crawl
work, focused sidecar state, anchor text, or exact next-visit times. Yago's
current document model has no typed collections, so YaCy collection and focused
policy values are retained as namespaced document metadata. Peer-facing RWI
postings are engine-specific and are not synthesized by this first slice.

The current count from a live Solr probe is not a migration guarantee. Verify
destination counts, representative local searches, metadata, URLs and any
supplemented schedules independently before deciding whether the remaining
source-state gaps are acceptable. Keep YaCy and its production data untouched
until that verification and a separate recoverable cutover plan are complete.
