# Search-demand-guided crawling

Search-demand-guided crawling can turn first-page public search activity into a
bounded background crawl. It is disabled by default and is controlled at
Configuration → Search. It does not change ordinary manual crawl profiles.

## What is retained

Eligible first-page searches through the public portal, YaCy-compatible search
routes, and the Tavily-compatible search API contribute a normalized query
topic. The normalized query is stored in the local vault for up to 28 days so
the crawler can repeat the search and find candidate pages. Client addresses
are used only to enforce daily limits and are represented in storage by a
keyed, rotating daily pseudonym; raw addresses and credentials are not retained
by this feature. Topic records are visible only in the authenticated Admin
Activity page.

Each resolved client address receives at most 20 accepted anonymous search
credits per day, or 100 when the request has valid read credentials. The same
address can contribute at most three credits to one topic per day. Repeated
result-page requests do not add credits. Searches for one topic on the same UTC
day are aggregated into one weighted daily bucket. The ledger retains at most
20,000 topics and 100,000 daily client pseudonyms. At either limit, searches
still work normally but new topics or client records are not added to the
crawl-demand ledger. Expired records are pruned on the 15-minute control loop.

## How crawl work is allocated

The node measures crawler fetch attempts, including attempts that fail, and
uses the average across completed UTC days with recorded attempts in the
preceding seven-day window as the observed daily capacity. Days without recorded
attempts are not included in that average. Demand topics share at most half of
that measured capacity. Their shares follow a seven-day half-life, and one
topic cannot take more than half of the demand pool. A new topic starts with a
maximum 25-page probe; larger daily allocations require evidence that pages
were indexed.

The 15-minute controller replays at most 100 due topics per pass, oldest
schedule attempt first. It records an attempt before searching so a failed
search cannot monopolize every pass; that topic becomes eligible again after
its 24-hour interval. The cap spreads large topic ledgers across later passes.

Search results from the local index and YaCy peers can contribute URLs; local
documents can also contribute their stored outlinks. External web results are
excluded unless **External web discovery for demand topics** is enabled. That
setting still uses the node's separately configured web-search provider and
its existing consent and safe-search rules.

Accepted URLs enter the ordinary durable crawl queue and use the existing
crawler's URL validation, deduplication, robots handling, host politeness,
resource controls, and indexing path. The feature does not create a second
fetcher or an independent index.

## Settings and status

- **Demand-guided crawling** (`search.demand.enabled`): off by default; applies
  immediately.
- **External web discovery for demand topics**
  (`search.demand.web_discovery`): off by default; applies immediately and has
  no effect unless demand-guided crawling is enabled.

Admin → Activity reports the observed capacity, retained topics, decayed
weights, daily fetch attempts, indexed-document evidence, and the last
scheduling attempt.
The capacity is a measurement of recent crawler activity, not a promise that a
particular future rate is safe. Keep the feature disabled until the node's
workload and desired crawl budget have been reviewed.
