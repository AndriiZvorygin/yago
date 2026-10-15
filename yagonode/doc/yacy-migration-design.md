# YaCy document migration design

## Source and destination paths

The production YaCy code is checked out at `/home/htaf/hven/yacy_search_server`;
its Docker data is mounted from `/srv/data/yacy`, with `DATA/INDEX` on
`/srv/search/yacy-index`. This work reads no production document rows and does
not change those paths.

YaCy's pack generator can write Elasticsearch bulk JSONL, but that endpoint
forces a minified field set and filters to HTTP 200 documents. `IndexExport_p`
exports URLs or text, not a complete document stream. The implementation uses
YaCy's read-only Solr `select` endpoint with cursor pagination and an explicit
stored-field list; it does not start an export job or write under the YaCy data
directory. Metadata-only runtime probes confirmed `cursorMark` support. The
reported count changed from 6,167,172 to 6,167,278 between probes, so the live
cursor is not a point-in-time snapshot. No document values were retained. The
embedded-Solr dump/restore page is not an interchange format and is not used.

The focused-crawler sidecar is under
`DATA/SETTINGS/focused/url-policy-metadata.heap`. It stores policy IDs and
versions, relevance, priority, collections, reason codes, depth, per-host
budget, recrawl interval, and novelty class. It has no documented bulk export
interface and does not store a per-URL next-visit timestamp. YaCy's URL metadata
and crawl queues are separate from the full-text export.

YagoSeek's destination owners are `yagocrawlcontract.DocumentIngest`,
`yagonode/internal/crawlresults`, `yagonode/internal/documentstore`, the search
index, URL metadata, and `yagonode/internal/recrawlfrontier`. The feature adds a
scoped `POST /admin/index/import` endpoint to the existing admin listener. It
submits bounded documents through the normal crawl-result consumer and its
document-store/search-index owners; it does not open Bleve or bbolt files
directly. The route inherits admin authentication and requires an `admin:write`
API key for bearer-token clients.

## Versioned record and field mapping

The command `yagonode/cmd/yacy-migrate` reads the existing embedded Solr
`select` endpoint using a stable `id` sort, `cursorMark`, and bounded `rows`; the
operator supplies the endpoint including `core=collection1`. It writes
versioned JSONL with a format header and one document record per line. Stable
record IDs derive from the non-sensitive export ID and YaCy's source document
ID (or normalized URL when the source ID is absent). The SHA-256 content hash
is computed over extracted text because YaCy's `exact_signature_l` is not a
cryptographic hash. The initial stored-field projection carries:

| YaCy export field | Yago destination |
| --- | --- |
| `sku` | source URL and normalized URL input |
| `canonical_s` | canonical URL |
| `title`, `h1_txt`–`h6_txt`, `text_t` | title, headings, extracted text |
| `language_s`, `content_type`, `httpstatus_i`, `failreason_s`, `load_date_dt`, `last_modified` | language, MIME type, fetch status, fetch time, failure status, modified time |
| `exact_signature_l` | retained as source metadata; it is not a cryptographic content hash |
| `outboundlinks_protocol_sxt` + `outboundlinks_urlstub_sxt` | outlink URLs and URL-only outbound-anchor records when both arrays are present and aligned |
| `inboundlinks_protocol_sxt` + `inboundlinks_urlstub_sxt` | URL-only inlink records when both arrays are present and aligned |
| `collection_sxt`, `focused_policy_sxt`, `focused_profile_version_sxt`, `focused_relevance_i`, `focused_priority_s`, `focused_reason_sxt`, `httpstatus_redirect_s` | namespaced source metadata; canonical URL is also mapped to its typed field |
| `nextVisitAt` in an optional supplemental record | Yago recrawl schedule, through `recrawlfrontier` |

When no source content hash is available, the adapter computes SHA-256 over the
extracted text. YaCy's 64-bit `exact_signature_l` is not treated as equivalent.

## Gaps and assumptions

- The built-in pack export contains only HTTP 200 records and its minified
  format omits most metadata. The read-only Solr query can include stored
  failure fields, but the URLDB and pending crawl-queue entries remain separate.
- YaCy's export has no complete alias history, per-document crawl-profile
  assignment, next-visit timestamp, or focused sidecar values. A supplemental
  record can carry these values when an operator has a supported source for
  them; the importer will not infer them from unrelated Solr fields.
- `fresh_date_dt` means a page is considered fresh until that time; it is
  preserved as source metadata and is not assumed to be an exact next-visit
  schedule.
- Webgraph records are stored separately. Only links present in the full-text
  document export can be migrated in this first slice.
- YaCy does not provide full inbound/outbound anchor text in the selected
  stored fields, so link URLs are preserved but link text is not. Yago's
  peer-facing RWI postings and YaCy URLDB wire metadata are engine-specific;
  this slice does not synthesize peer postings. Imported documents are locally
  stored and searchable, but this is not a complete P2P-index migration.
- Yago's current document model has no typed collection or alias fields. The
  adapter preserves those source values as namespaced document metadata rather
  than silently dropping them or changing search semantics.
- Yago currently accepts at most 1 MiB of extracted text per document, 256
  headings, 4,096 outlinks, 1,024 anchors, and 8 KiB of metadata. The exporter
  fails clearly on a row that cannot fit; it does not truncate or silently skip
  content. Such rows require an explicit later contract change before complete
  migration can be claimed.
- A row without a next-visit value is imported as completed content but is not
  scheduled for recrawl. A row with a next-visit value gets a durable schedule;
  only due schedules enter the recrawl frontier. Import itself does not submit
  URLs to the new-crawl queue.

This provides a verifiable content migration and schedule preservation when
schedule data is supplied. It does not claim to migrate YaCy's pending queue or
focused sidecar from the current production files; those require a source-side
read-only export interface before a complete crawl-state cutover can be proven.

## Recovery and idempotency

The exporter checkpoints the last committed Solr cursor and output byte offset;
on restart it truncates only its own output artifact to that committed offset
before resuming. The importer checkpoints the interchange file identity and
completed byte offset using atomic replacement and filesystem sync. Export
pages, import HTTP batches, and input lines have fixed upper bounds; destination
ingest backpressure is synchronous. Restarting after a timeout retries the
same batch. Existing normalized-URL/content matches return `already-present`;
different content at the same normalized URL returns `conflict` without
replacement. The normalized URL is the record identity: distinct URL rows are
retained even when they share a canonical URL or identical content, so alias
and link evidence is not discarded. A fresh complete export and
import can be used as a reconciliation pass; use a new output/checkpoint path
and the same stable source ID. Because Solr cursor iteration is live rather than
snapshot-isolated, repeat reconciliation until the source and exported counts
are stable before any cutover. `--dry-run` validates/maps source records without
writing an output or checkpoint and validates import input without contacting
Yago. Import never enqueues an ordinary crawl; only an explicitly provided,
durable `nextVisitAt` schedule can reach the recrawl frontier, and then only
when due.
