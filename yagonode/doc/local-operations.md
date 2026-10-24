# Local node status

Run `tools/yago-status` from the repository root to inspect the configured local
Yago node. It reports the running image and source revision, current checkout,
container resource use, mounted filesystem capacity, `/ready`, the public
endpoint, RWI backfill progress, and counts of selected recent log events.

The default container name is `yago-yacy-migration-full`. Select another local
container with `tools/yago-status --container NAME`. Add `--json` for a
machine-readable report.

The command reads administrator credentials from the container configuration
only to request authenticated RWI status. It does not print credentials,
cookies, raw error details, or log lines. A successful request creates and then
invalidates a short-lived admin session. If authentication or the operations
listener is unavailable, the report identifies the failed step; that does not
imply that local search is unavailable.

`/ready` reports the local search index only. A ready search index does not prove
that RWI storage, backfill, the crawler, or peer connections are healthy. Use
the component fields and counters in the local report to distinguish those
states. The command does not change the index, crawl queue, peer identity, or
node configuration.
