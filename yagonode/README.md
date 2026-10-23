# YaGo node

`yago-node` serves the local search index, the YaCy-compatible peer protocol,
the public search and Tavily-compatible APIs, the Admin console, and the node
side of the durable crawler queue. The node and `yago-crawler` are separate
processes; parsed pages return through the bounded ingest contract.

Configuration and operator behavior are documented in [`doc/configuration.md`](doc/configuration.md).
Optional search-demand-guided crawling is described in
[`doc/search-demand-crawling.md`](doc/search-demand-crawling.md). That feature
is disabled by default and does not replace ordinary crawl profiles.

Invitation-only account personalization is disabled by default and described
in [`doc/personalized-search.md`](doc/personalized-search.md). It reranks only
the signed-in user's retrieved results and keeps personal history on this node.

The public listener root provides a responsive search home with local-index and
YaCy-network scope choices. Search results use the same accessible light/dark
theme and remain available through `/yacysearch.html` without JavaScript.
When an eligible first-page plain query reaches the primary full-text deadline,
the node uses the reserved final portion of the same request budget to search
shards in a per-request OS-cryptographic-random order. The browser can render
shard progress as it arrives; agents can consume NDJSON from `/search/stream`
or `/yacysearch.ndjson`. Large shards return a bounded sample from a randomized
internal-ID position; coverage reports sampled separately from fully searched
shards. Responses expose completeness so a partial result is not mistaken for
an exact empty answer. The ordinary full-text alias search remains the primary
path.
