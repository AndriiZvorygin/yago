# YaGo node

`yago-node` serves the local search index, the YaCy-compatible peer protocol,
the public search and Tavily-compatible APIs, the Admin console, and the node
side of the durable crawler queue. The node and `yago-crawler` are separate
processes; parsed pages return through the bounded ingest contract.

Configuration and operator behavior are documented in [`doc/configuration.md`](doc/configuration.md).
Optional search-demand-guided crawling is described in
[`doc/search-demand-crawling.md`](doc/search-demand-crawling.md). That feature
is disabled by default and does not replace ordinary crawl profiles.
