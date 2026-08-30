# Browser smoke test

Drives a real headless Chrome against a running store and checks that the three
tabs still do what they are for. Not a unit test, not run in CI.

```bash
docker compose up -d --build app     # something to test against
node --test web/smoke/*_test.mjs
```

Takes about ten seconds. No dependencies to install — it talks to Chrome over
the DevTools Protocol with the WebSocket built into node, the same way the rest
of this repo's frontend testing needs nothing but node.

## Why this exists

Splitting `app.js` into nine modules (#124) produced four real breaks, and
`node --check`, `go test ./...` and `bazel test //...` passed **every one**:

| Break | How it showed up |
|---|---|
| a module missing an import it used | a `ReferenceError` on the control that reached the missing name |
| `search.js` missing `updateUtcPreview` | a `ReferenceError` on clearing filters |
| A stray `}` in `app.js` | the whole page dead, `node --check` silent |
| `loadIdentity` moved into the wrong module | startup died at `/me`, every tab inert |

None of them is a logic error a unit test would have found. They are the kind of
mistake that only appears when the page actually runs, which is what this does.

Removing `EVIDENCE_TYPES` from `search.js`'s imports still fails the deep-link
check, which is the version of this test that is worth keeping.

## What it covers

- the page starting up: four tabs wired, the build named in the footer, the
  first search returning
- search: filtering, paging, opening a record, the back button, clearing
- Add Result: the current time and its UTC preview, a custom metadata row,
  markdown in the log preview, weather written by hand — and the record arriving
  in the store with all of it attached
- the outbox: filing with no connection, correcting what is waiting, and the
  queue sending itself when the connection returns

Each check starts on a freshly loaded page. They share one browser but no state,
because when they shared state a break in one turned into three red tests and no
obvious cause.

Any check also fails if the page logged an uncaught exception or a
`console.error`, whether or not the check thought to look for it. Three of the
four breaks above announced themselves that way and nowhere else.

## What it leaves behind

It files records, and there is no DELETE endpoint. Everything it writes goes
under one repo, so:

```bash
docker exec evidence_store-db-1 psql -U evidence -d evidence_store \
  -c "DELETE FROM evidence WHERE repo = 'smoke/browser-check';"
```

It also clears the browser profile it creates, service workers and the outbox
database — in its own throwaway profile, never in yours.

## Settings

| Variable | Default | |
|---|---|---|
| `SMOKE_BASE_URL` | `http://localhost:8000` | the store to test |
| `CHROME` | first of the usual paths that exists | the browser binary |
| `SMOKE_CDP_PORT` | `9223` | debugging port, kept off 9222 so it does not collide with a browser you are already driving |

## The `node --check` trap

`node --check` **silently accepts** a syntax error in any file containing
`import`:

```bash
printf 'import { x } from "./y.js";\nfunction a() {}\n;\n}\n' > /tmp/broken.js
node --check /tmp/broken.js    # exit 0, no output
cp /tmp/broken.js /tmp/broken.mjs
node --check /tmp/broken.mjs   # SyntaxError: Unexpected token '}'
```

To syntax-check the modules in `web/static`, copy them to `.mjs` first.
