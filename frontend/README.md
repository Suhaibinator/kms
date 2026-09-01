# Frontend development

The KMS console is a Next.js application that ships as a static export in
`frontend/out`. The Go build embeds that directory into the `parameter-store`
binary; there is no Next.js server in production.

## Local development

Install the locked dependencies:

```bash
cd frontend
npm ci
```

Start the Go HTTP server on `localhost:8080`, then run:

```bash
npm run dev
```

The Next.js development server proxies `/api/*` to
`http://localhost:8080/api/*`. Open the URL printed by Next.js and sign in with
an identity token. See the root [quickstart](../README.md#initialize-and-run-locally)
for a local server setup.

## Checks and tests

Run the source, component, and production-build gates with:

```bash
npm run check
```

This runs generated route types, TypeScript, Biome lint/format checks, Vitest,
and the static export. Browser tests are separate because they require
Chromium:

```bash
npx playwright install chromium # first run only
npm run test:e2e
```

Playwright starts its own Next.js development server and intercepts the JSON
API with in-memory fakes. It does not require a running Go server. See
[`docs/testing.md`](../docs/testing.md#frontend) for fixture ownership and CI
boundaries.

## Production export and preview

Build the files embedded by Go with:

```bash
npm run build
test -f out/index.html
```

From the repository root, `make frontend` performs the locked install and the
same build. `next start` is not a valid preview command when Next.js uses
`output: "export"`; serve `out/` with a static file server for a frontend-only
preview, or run `make build` and start the resulting binary as described in the
root quickstart to exercise the deployed routing behavior.

All runtime data comes from the JSON API documented in
[`docs/http-api.md`](../docs/http-api.md).
