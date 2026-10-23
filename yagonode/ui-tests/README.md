# YagoSeek browser acceptance tests

These tests exercise the public search form, a repeated streaming search, and an
unmatched query in Chromium and Firefox. They save screenshots under the ignored
`test-results/` directory and never store result text in fixtures.

Run them against a local YagoSeek node after starting that node:

```sh
npm ci
npx playwright install chromium firefox
npm test
```

Set `YAGO_UI_BASE_URL` when the public search listener is not at
`http://127.0.0.1:8080`. Set
`YAGO_UI_QUERY` to choose a known query with results in the local index. The
default query is `Canada`. If the node requires HTTP Basic Authentication, pass
`YAGO_UI_USERNAME` and `YAGO_UI_PASSWORD` to the test process through a private
environment file; do not put credentials in this directory or its fixtures.

For repeatable Linux runs, use the version-matched official Playwright image
`mcr.microsoft.com/playwright:v1.64.1-noble`, which includes the browser
binaries. Tests are sequential because they exercise one local search node.
