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
For an unconstrained first-page local search of up to four plain words that
reaches the interactive deadline, the node can return title-only matches from
the same Bleve index. Such a response is explicitly marked incomplete; it is
not a complete full-text result set. The normal full-text search remains
primary.
