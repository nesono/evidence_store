# Evidence Store

A backend service for ingesting, storing, and querying test evidence from heterogeneous sources (Bazel test logs, CI pipelines, manual test runs, HiL/PiL/vehicle tests).

Evidence Store provides a unified API to collect and query test results across different tools and workflows. It supports batch ingestion, cursor-based pagination, evidence inheritance across commits, configurable retention policies, and a web UI for manual test entry and search with regex filtering.

- **[Setup and administration](#setup-and-administration)** — running and configuring a deployment
- **[Bazel adapter](#bazel-adapter)** — wiring it into a workspace, CI, and a developer's own machine
- **[Web interface](#web-interface)** — manual test entry, search, inheritance, analytics
- **[Contributing](#contributing)** — building, testing and releasing this repo
- **[HTTP API reference](docs/api-reference.md)** — every endpoint, parameter and payload

## TL;DR

```bash
# Start Postgres + the server on :8000
docker compose up -d
curl http://localhost:8000/healthz

# Open the web UI
open http://localhost:8000
```

```bash
# In this repo: run our own tests and upload the results through the adapter
./scripts/dogfood.sh
```

To upload results from **any other Bazel workspace**, add the adapter as a
dependency and run it after your tests, the same way CI or `dogfood.sh` does:

```starlark
# MODULE.bazel
bazel_dep(name = "evidence_store_bazel", version = "0.0.1")

git_override(
    module_name = "evidence_store_bazel",
    remote = "https://github.com/nesono/evidence_store.git",
    commit = "<pinned-sha>",
    strip_prefix = "adapters/bazel",
)
```

```bash
bazel test //...
bazel run @evidence_store_bazel//cmd/evidence-bazel -- \
    --api-url http://localhost:8000 \
    --testlogs-dir "$(bazel info bazel-testlogs)"
```

See [Bazel adapter](#bazel-adapter) below for CI integration (GitHub Actions)
and an always-on watch mode for a developer's own machine.

The rest of this document explains those commands and everything around them.

## Setup and administration

### Quick start

```bash
docker compose up -d
curl http://localhost:8000/healthz
```

This starts PostgreSQL 16, the Evidence Store server on port 8000, and (for the
`s3` blob backend) a local MinIO. The web UI is served from the same port —
open `http://localhost:8000`.

### Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `EVIDENCE_DATABASE_URL` | `postgres://evidence:evidence@localhost:5432/evidence_store?sslmode=disable` | PostgreSQL connection string |
| `EVIDENCE_LISTEN_ADDR` | `:8000` | Listen address |
| `EVIDENCE_LOG_LEVEL` | `INFO` | Log level |
| `EVIDENCE_DEFAULT_PAGE_SIZE` | `100` | Default page size |
| `EVIDENCE_MAX_PAGE_SIZE` | `1000` | Max page size |
| `EVIDENCE_MAX_BATCH_SIZE` | `1000` | Max records per batch |
| `EVIDENCE_ANALYTICS_CACHE_TTL_SECONDS` | `30` | How long an analytics aggregation is reused for an identical filter (`0` disables) |
| `EVIDENCE_ANALYTICS_QUERY_TIMEOUT_SECONDS` | `15` | Budget for one analytics aggregation before it is refused (`0` disables) |
| `EVIDENCE_API_KEYS` | *(empty — auth disabled)* | Comma-separated API keys (see [Authentication](#authentication-and-authorization)) |
| `EVIDENCE_AUTH_DB` | `false` | Authenticate against the `principals` table (see [Database-backed principals](#database-backed-principals)) |
| `EVIDENCE_BOOTSTRAP_ADMIN` | *(empty)* | Subject of an administrator seeded on first start; its key is logged once |
| `EVIDENCE_OIDC_ISSUER` | *(empty — SSO off)* | Identity provider to log people in with (see [Single sign-on](#single-sign-on)) |
| `EVIDENCE_OIDC_CLIENT_ID` | *(empty)* | Client id registered with the provider |
| `EVIDENCE_OIDC_CLIENT_SECRET` | *(empty)* | Client secret, for a confidential client |
| `EVIDENCE_OIDC_REDIRECT_URL` | *(empty)* | Where the provider sends the browser back, e.g. `https://evidence.example.com/auth/callback` |
| `EVIDENCE_OIDC_SCOPES` | `openid,profile,email` | Scopes to request |
| `EVIDENCE_OIDC_GROUPS_CLAIM` | `groups` | Claim carrying group membership (Entra calls it `roles`) |
| `EVIDENCE_GROUP_ROLE_MAP` | *(empty)* | `group:role` pairs for either provider, e.g. `eng-all:contributor,eng-leads:admin`. `EVIDENCE_OIDC_ROLE_MAP` is still read as a fallback |
| `EVIDENCE_SAML_IDP_METADATA_URL` | *(empty — SAML off)* | Identity provider metadata to fetch at startup (see [SAML](#saml)) |
| `EVIDENCE_SAML_IDP_METADATA_FILE` | *(empty)* | The same metadata from a file, for a deployment that will not reach out |
| `EVIDENCE_SAML_ROOT_URL` | *(empty)* | This store's public address, e.g. `https://evidence.example.com` |
| `EVIDENCE_SAML_ENTITY_ID` | *(metadata URL)* | What this service provider calls itself |
| `EVIDENCE_SAML_CERT_FILE` | *(empty)* | Service provider certificate, PEM |
| `EVIDENCE_SAML_KEY_FILE` | *(empty)* | Service provider private key, PEM |
| `EVIDENCE_SAML_EMAIL_ATTRIBUTE` | `email` | Assertion attribute carrying the address |
| `EVIDENCE_SAML_NAME_ATTRIBUTE` | `displayName` | Assertion attribute carrying the display name |
| `EVIDENCE_SAML_GROUPS_ATTRIBUTE` | `groups` | Assertion attribute carrying group membership |
| `EVIDENCE_SESSION_TTL_HOURS` | `12` | How long a login lasts |
| `EVIDENCE_COOKIE_SECURE` | `true` | `false` only for local development over plain HTTP |
| `EVIDENCE_RATE_LIMIT_READ_RPS` | `0` (disabled) | Sustained reads per second per caller (see [Rate limiting](#rate-limiting)) |
| `EVIDENCE_RATE_LIMIT_WRITE_RPS` | `0` (disabled) | Sustained writes per second per caller |
| `EVIDENCE_RATE_LIMIT_READ_BURST` | `2 × read RPS` | Token-bucket burst capacity for reads |
| `EVIDENCE_RATE_LIMIT_WRITE_BURST` | `2 × write RPS` | Token-bucket burst capacity for writes |
| `EVIDENCE_BLOB_BACKEND` | `fs` | Where images live: `fs` or `s3` (see [Images in test logs](docs/api-reference.md#images-in-test-logs)) |
| `EVIDENCE_BLOB_PATH` | `blobs` | Directory for the `fs` backend |
| `EVIDENCE_BLOB_S3_ENDPOINT` | *(empty)* | `host:port` of the S3/MinIO endpoint |
| `EVIDENCE_BLOB_S3_BUCKET` | `evidence-blobs` | Bucket to store blobs in |
| `EVIDENCE_BLOB_S3_ACCESS_KEY` | *(empty)* | S3 access key |
| `EVIDENCE_BLOB_S3_SECRET_KEY` | *(empty)* | S3 secret key |
| `EVIDENCE_BLOB_S3_USE_SSL` | `false` | `true` to talk to the endpoint over HTTPS |
| `EVIDENCE_BLOB_S3_REGION` | *(empty)* | S3 region |
| `EVIDENCE_MAX_BLOB_BYTES` | `5242880` (5 MiB) | Largest image that may be uploaded |
| `EVIDENCE_BLOB_ORPHAN_GRACE_HOURS` | `24` | How long an unreferenced image is kept before the sweep removes it |
| `EVIDENCE_RETENTION_CONFIG` | *(empty — retention off)* | Path to a retention rules YAML file (see [Retention](#retention)) |
| `EVIDENCE_WEATHER_ENDPOINT` | `https://api.open-meteo.com/v1/forecast` | Forecast API the weather lookup asks. Set it to an empty value to switch the lookup off (see [Weather while a test ran](docs/api-reference.md#weather-while-a-test-ran)) |
| `EVIDENCE_WEATHER_TIMEOUT_SECONDS` | `10` | Budget for one weather lookup before the tester is told to type the conditions in |

### Authentication and authorization

Set `EVIDENCE_API_KEYS` to enable API key authentication for all `/api/v1/*` endpoints. The `/healthz` endpoint and static web UI files are always public.

Each key entry has the format `role:key` where role is `rw` (read-write) or `ro` (read-only):

```bash
# Single read-write key
export EVIDENCE_API_KEYS="rw:my-secret-key"

# Multiple keys with different roles
export EVIDENCE_API_KEYS="rw:ingest-key-for-ci,ro:dashboard-viewer-key"
```

- **`rw`** keys can read and write (GET + POST).
- **`ro`** keys can only read (GET). POST requests return `403 Forbidden`.
- Requests without a valid key return `401 Unauthorized`.
- When `EVIDENCE_API_KEYS` is empty or unset, authentication is disabled (open access).

Behind those two key roles the server authorizes by permission, not by HTTP
method. Authentication resolves the caller to a **principal** holding one or
more **roles**, and every route states the **permission** it needs:

| Role | Permissions |
|---|---|
| `viewer` | `evidence:read`, `analytics:read`, `blob:read`, `inheritance:read` |
| `contributor` | `viewer` + `evidence:write`, `blob:write` |
| `ci` | `contributor` + `source:any` (may write a `source` that is not its own name) |
| `admin` | `contributor` + `inheritance:write`, `principal:admin`, `retention:admin` |

`POST /inheritance` requires `inheritance:write`, which only `admin` holds —
declaring that one commit inherits another's evidence is the elevated operation
[DESIGN.md](DESIGN.md) section 8 has always specified, and it used to be
indistinguishable from posting a test result.

Configured keys map onto those roles so that nothing that worked before stops
working: `ro` becomes `viewer`, and `rw` becomes `ci` **and** `admin`, since an
`rw` key can reach every endpoint today. To grant the finer roles individually,
give keys names and owners, or revoke one without a redeploy, use
database-backed principals below.

Clients authenticate by sending the key as a Bearer token:

```bash
curl -H "Authorization: Bearer my-secret-key" \
  http://localhost:8000/api/v1/evidence
```

The Bazel adapter supports this via `--api-key` or `EVIDENCE_STORE_API_KEY` (see [Bazel adapter](#bazel-adapter)). The web UI prompts for a key on first 401 and stores it in `localStorage`.

### Database-backed principals

An entry in `EVIDENCE_API_KEYS` is a shared secret: no name, no owner, no
expiry, and no way to revoke one short of a redeploy. Setting
`EVIDENCE_AUTH_DB=true` turns on the `principals` table instead, where a key
belongs to somebody, holds exactly the roles it was granted, and stops working
on the next request when it is disabled.

| Variable | Default | Description |
|---|---|---|
| `EVIDENCE_AUTH_DB` | `false` | `true` to authenticate bearer tokens against the `principals` table |
| `EVIDENCE_BOOTSTRAP_ADMIN` | *(empty)* | Subject of an administrator seeded on first start, e.g. `user:ops@example.com`. Requires `EVIDENCE_AUTH_DB=true` |

Switching it on **closes the API**. An empty `principals` table means nobody may
in, not that everybody may — so seed the first administrator at the same time:

```bash
export EVIDENCE_AUTH_DB=true
export EVIDENCE_BOOTSTRAP_ADMIN="user:ops@example.com"
```

On first start the server mints that principal a key and logs it once:

```
WARN bootstrap admin API key issued - copy it now, it is not stored and will not
     be shown again subject=user:ops@example.com api_key=evs_...
```

Only a SHA-256 digest of the key is stored, so that line is the single moment it
can be read. Miss it and the remedy is to rotate the key (see below), which
keeps the identity and its roles. Restarts are safe: an existing subject is left
alone and no second key is minted.

Keys are always minted by the server, never chosen by a caller — 256 bits from
`crypto/rand`, prefixed `evs_`. That is what makes a fast digest the right one
to store; see the comment on `principals.key_hash` in
[migration 000006](migrations/000006_add_rbac.up.sql).

**Both key sources run at once**, which is the migration path: leave
`EVIDENCE_API_KEYS` in place, issue database keys, move pipelines over one at a
time, and clear the variable when the last has moved. Environment keys are
checked first because they cost no round trip. A token neither source
recognises is `401`; if the database cannot be reached at all, requests get
`503` rather than being told their key is wrong.

#### Issuing and revoking keys

The **Access** tab in the web UI is the everyday way in: it lists every
principal with its roles, when its key was last used, and whether it has been
revoked, and it issues, rotates and revokes keys. The tab is only shown to a
caller holding `principal:admin`. See [Managing access](#managing-access) for
what the tab looks like.

The same operations are `/api/v1/principals`, all behind `principal:admin`:

| Request | Does |
|---|---|
| `GET /api/v1/principals` | List every principal, revoked ones included |
| `POST /api/v1/principals` | Create one and mint its key — `{"subject": "ci:nightly", "display_name": "…", "roles": ["ci"]}` |
| `PUT /api/v1/principals/{id}/roles` | Set roles to exactly `{"roles": [...]}` |
| `POST /api/v1/principals/{id}/disable` | Revoke: the key stops working on the next request |
| `POST /api/v1/principals/{id}/enable` | Restore, with the roles it already had |
| `POST /api/v1/principals/{id}/rotate` | Issue a fresh key and invalidate the old one |

Creating and rotating return `{"principal": {...}, "api_key": "evs_..."}`. That
response is the only time the key can be read — only its digest is stored — so
a mislaid key is fixed by rotating, not by looking it up.

There is **no delete**. Revoking is a timestamp so that evidence already
attributed to a principal still names something a reader can look up, and so an
administrator can tell a credential that was taken away from one that never
existed.

The last enabled administrator cannot be revoked or demoted; the request is
refused with `409` and a message saying to grant `admin` to somebody else first.
Otherwise one click could leave a deployment with no way in but `psql`.

`GET /api/v1/me` reports the calling principal, its roles and its permissions.
It is the one route under `/api/v1` that asserts no permission of its own — the
web UI uses it to decide what to offer.

### Single sign-on

Point the store at your identity provider and people log in with the account
they already have. Set `EVIDENCE_OIDC_ISSUER` and the client credentials it
issued you:

```bash
export EVIDENCE_AUTH_DB=true            # sessions resolve to principals
export EVIDENCE_OIDC_ISSUER="https://login.example.com/realms/engineering"
export EVIDENCE_OIDC_CLIENT_ID="evidence-store"
export EVIDENCE_OIDC_CLIENT_SECRET="…"
export EVIDENCE_OIDC_REDIRECT_URL="https://evidence.example.com/auth/callback"
export EVIDENCE_GROUP_ROLE_MAP="eng-all:contributor,eng-leads:admin"
```

Register the redirect URL with the provider, and make sure the ID token carries
a groups claim — in Keycloak that is a *group membership* mapper, and most
providers need it switched on explicitly.

The flow is Authorization Code with PKCE. `GET /auth/login` sends the browser
out, `GET /auth/callback` verifies the ID token and starts a session, and
`POST /auth/logout` ends it. The session is a **row**, not a signed cookie, so
revoking somebody stops the browser they left open rather than waiting for a
token to expire. `GET /auth/config` reports whether a login flow exists, which
is how the UI knows to offer one.

**Roles come from groups.** A group with no entry in `EVIDENCE_GROUP_ROLE_MAP`
grants nothing, so pointing this store at a company directory does not hand
every employee an account that can write. On each login the roles derived from
group claims are reconciled to what the token now says — losing a group loses
the role — while roles an administrator granted in the **Access** tab are left
alone. Someone whose groups map to nothing is authenticated and permitted
nothing, which is a deliberate state and not an error.

**People are matched on the provider's `sub` claim**, not their address. Someone
who changes their email stays one principal with their history intact; their
`subject` here is corrected at the next login. If an API key already answers to
the name a login wants, the login is refused with `409` rather than guessing
they are the same party — rename the key.

**Writes need a CSRF token.** A cookie is sent by the browser whether or not the
page meant to send it, so session-authenticated writes must echo the
`evidence_csrf` cookie in an `X-CSRF-Token` header. Bearer-token callers are
unaffected: CI has no cookies and needs none of this.

Signing in also settles the `source` question for humans. A logged-in person's
Source box is filled in with their own subject and locked, because that is the
only value the server will accept from them — see
[`source` is bound to the caller](#source-is-bound-to-the-caller).

### SAML

Same idea, different protocol, and the same everything else — a SAML login
produces the identical principal, session, roles and `source` binding an OIDC
one does.

```bash
export EVIDENCE_AUTH_DB=true
export EVIDENCE_SAML_IDP_METADATA_URL="https://login.example.com/app/xxx/sso/saml/metadata"
export EVIDENCE_SAML_ROOT_URL="https://evidence.example.com"
export EVIDENCE_SAML_CERT_FILE=/etc/evidence/saml.crt
export EVIDENCE_SAML_KEY_FILE=/etc/evidence/saml.key
export EVIDENCE_GROUP_ROLE_MAP="eng-all:contributor,eng-leads:admin"
```

A service provider needs its own X.509 keypair, which most identity providers
will not register one without. A self-signed pair is enough:

```bash
openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
  -keyout saml.key -out saml.crt -subj "/CN=evidence.example.com"
```

Then point the provider at `https://evidence.example.com/auth/saml/metadata`,
which describes this store in the XML they expect — where assertions go and
which certificate signs requests. Serving it beats writing it by hand, which is
how the two ends end up disagreeing about a URL.

`GET /auth/saml/login` starts a login and `POST /auth/saml/acs` is the Assertion
Consumer Service the provider posts back to. Logging out is the same
`POST /auth/logout` either way.

Attribute names vary a great deal between providers, so the three that matter
are configurable, and the common spellings — `mail`, `cn`, `memberOf`, and the
`urn:oid:` and `schemas.xmlsoap.org` forms Entra and ADFS send — are tried
automatically when the configured name is not present.

**Both front ends can run at once.** A company moving between protocols will
have a period where each is somebody's way in, so the UI offers a choice when
two are configured and goes straight there when there is one.

One detail worth knowing if you are reading the schema: an assertion arrives as
a form POST from the provider's own origin, and a `SameSite=Lax` cookie is
exactly what a browser will not send on a cross-site POST. The id of the request
it answers therefore lives in a `saml_requests` row rather than a cookie —
loosening the session cookie to `SameSite=None` to avoid that table would have
traded a real protection for a detail of one flow.

See [docs/rbac-design.md](docs/rbac-design.md) for the whole plan, including
where SSO/SAML plugs in.

#### `source` is bound to the caller

A record's `source` is what a reader goes on months later to ask who ran this
and whether to believe them, so the server decides what it may say:

| Caller | `source` |
|---|---|
| `ci` (holds `source:any`) | Taken as sent — a build robot's useful attribution is the build URL, not the robot |
| Any other principal | Must equal the caller's subject. Left empty, the server fills it in; anything else is `403` |
| No principal (nothing configured) | Unchanged — there is no identity to pin it to |

In a batch, one record with a source the caller may not write refuses the whole
batch. A *malformed* record still behaves as it always did: reported in the
per-record results, with the rest of the batch filed and a `207`.

`admin` does not subsume `ci`, so an administrator is pinned to their own name
too. Backfilling evidence in somebody else's name means holding both roles, so
that writing history under another party's identity is always a deliberate
grant. Configured `EVIDENCE_API_KEYS` are unaffected: every `rw` key is `ci`, so
the pipelines using one keep writing exactly the `source` they wrote before.

### Rate limiting

Set `EVIDENCE_RATE_LIMIT_READ_RPS` and/or `EVIDENCE_RATE_LIMIT_WRITE_RPS` to enforce per-caller token-bucket limits on `/api/v1/*` endpoints. Reads (GET/HEAD/OPTIONS) and writes (POST/PUT/PATCH/DELETE) use separate buckets so a flood of reads cannot starve writes.

```bash
# 50 reads/sec sustained (burst 100), 10 writes/sec sustained (burst 20).
export EVIDENCE_RATE_LIMIT_READ_RPS=50
export EVIDENCE_RATE_LIMIT_WRITE_RPS=10
```

- Authenticated callers are bucketed by API key; unauthenticated callers by IP (`X-Forwarded-For` honored).
- When a limit is exceeded the server returns `429 Too Many Requests` with a `Retry-After` header (seconds).
- Limits are in-memory and per process — deployments running multiple replicas should add a shared limiter (e.g. Redis) if precise global enforcement is required.

### Retention

Old evidence can be evicted automatically instead of growing the database
forever. Point `EVIDENCE_RETENTION_CONFIG` at a YAML file and a background
worker applies it on an interval:

```bash
export EVIDENCE_RETENTION_CONFIG=/etc/evidence/retention.yaml
```

See [retention.example.yaml](retention.example.yaml) for a starting point —
rules match evidence by regex on fields like `branch` or `evidence_type` and
give each a `max_age` (`0s` keeps it forever), evaluated highest-`priority`
first. Records with `metadata.retain=true`, or referenced by an active
[inheritance declaration](#inheriting-test-results-between-commits), are always
exempt. Deleting a record releases the images its log referenced (see
[Images in test logs](docs/api-reference.md#images-in-test-logs)). The full
rule syntax and rationale are in
[docs/retention-rules-plan.md](docs/retention-rules-plan.md). Retention runs
default off — an unset variable means nothing is ever evicted.

## Bazel adapter

Scans `bazel-testlogs/` after a test run and uploads results to the Evidence
Store. It lives in `adapters/bazel/` as its own Bzlmod module named
`evidence_store_bazel`, so other Bazel workspaces can depend on it without
pulling in the server's own dependencies.

### Add it to a workspace

From the consuming repo's `MODULE.bazel`:

```starlark
bazel_dep(name = "evidence_store_bazel", version = "0.0.1")

git_override(
    module_name = "evidence_store_bazel",
    remote = "https://github.com/nesono/evidence_store.git",
    commit = "<pinned-sha>",
    strip_prefix = "adapters/bazel",
)
```

For local development against a checkout instead:

```starlark
local_path_override(
    module_name = "evidence_store_bazel",
    path = "/path/to/evidence_store/adapters/bazel",
)
```

Building it inside this repo (rather than as a dependency) is:

```bash
cd adapters/bazel
bazel build //cmd/evidence-bazel
```

### CI integration

A build-engineer setup: run the suite, then upload the results — pass or fail —
so a broken run still leaves a record. A GitHub Actions job for a *consuming*
workspace looks like this:

```yaml
name: CI
on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
      - uses: bazel-contrib/setup-bazel@0.19.0

      - name: Run tests
        run: bazel test //... || true   # keep going so results still upload

      - name: Upload results to Evidence Store
        env:
          EVIDENCE_STORE_API_KEY: ${{ secrets.EVIDENCE_STORE_API_KEY }}
        run: |
          bazel run @evidence_store_bazel//cmd/evidence-bazel -- \
            --api-url https://evidence.example.com \
            --testlogs-dir "$(bazel info bazel-testlogs)" \
            --source "${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}" \
            --tags ci,github-actions
```

`--repo`, `--branch` and `--rcs-ref` are auto-detected from the git checkout, so
they are left out above. Mint `EVIDENCE_STORE_API_KEY` as a `ci`-role principal
(see [Authentication and authorization](#authentication-and-authorization)) — it
needs `source:any` to attribute records to the build URL rather than to itself.

### Using it on your own machine

For a one-off run, `bazel test` then feed the adapter the resulting logs:

```bash
bazel test //...
bazel run //cmd/evidence-bazel -- \
    --api-url http://localhost:8000 \
    --testlogs-dir "$(bazel info bazel-testlogs)"
```

Or, from inside this repo, the dogfood script that does both:

```bash
./scripts/dogfood.sh
```

| Flag | Default | Description |
|------|---------|-------------|
| `--api-url` | `$EVIDENCE_STORE_URL` | API base URL (required) |
| `--testlogs-dir` | `bazel-testlogs` | Path to testlogs directory |
| `--repo` | auto-detected | Repository (from git remote) |
| `--branch` | auto-detected | Branch (from git) |
| `--rcs-ref` | auto-detected | Commit hash (from git HEAD) |
| `--source` | auto-detected | CI build URL or username |
| `--invocation-id` | | Bazel invocation ID |
| `--tags` | | Comma-separated tags |
| `--api-key` | `$EVIDENCE_STORE_API_KEY` | API key |
| `--dry-run` | `false` | Print records instead of posting |

For continuous, hands-off uploads on your workstation — no changes to your
`bazel test` habit needed — run the adapter as a background watcher instead:

```bash
# One-time setup: create .evidence/config.yaml in your workspace
mkdir -p .evidence
cat > .evidence/config.yaml <<EOF
api_url: https://evidence.mycompany.com
tags: [local, dev]
EOF

# Start the watcher (runs in background)
bazel run @evidence_store_bazel//cmd/evidence-bazel -- watch start

# Check status
bazel run @evidence_store_bazel//cmd/evidence-bazel -- watch status

# Stop
bazel run @evidence_store_bazel//cmd/evidence-bazel -- watch stop
```

The watcher polls `bazel-testlogs/` every 5 seconds, waits for Bazel to finish
(lock released), then uploads only new/changed results. It reads config from
`.evidence/config.yaml` and environment variables (`EVIDENCE_STORE_URL`,
`EVIDENCE_STORE_API_KEY`). Logs go to `.evidence/watch.log`; use `--foreground`
with `watch start` to run in the foreground instead, for debugging.

The commands above assume a consumer workspace with `evidence_store_bazel`
added as a `bazel_dep`; replace `@evidence_store_bazel//cmd/evidence-bazel`
with `//cmd/evidence-bazel` when running from inside `adapters/bazel/` in this
repo.

### Recording ad-hoc results

Not all test workflows produce a JUnit `test.xml`. Failure tests (where a
`bazel build` is *expected* to fail with a specific stderr pattern) and
shell-driven integration tests determine pass/fail outside Bazel's test runner.
For these, use the `record` subcommand to emit a single evidence record with an
externally-determined verdict:

```bash
bazel run @evidence_store_bazel//cmd/evidence-bazel -- record \
    --procedure-ref "//fire/starlark/failure_test:version_too_old_basic" \
    --result PASS \
    --notes "expected 'static_assert' pattern found in stderr" \
    --tags failure_test,version_too_old
```

`--evidence-type` defaults to `ci` — a failure test is still something a machine
ran — and accepts only `ci`, `manual_test` or `demonstration`. It is checked
before anything is uploaded, so a wrong value fails the command instead of being
rejected record by record halfway through a loop. What kind of automated test it
was belongs in `--tags`, which is where it is filterable anyway.

Unlike the ingest and watch paths, `record` does not set `metadata.collector`:
a verdict decided outside Bazel's test runner was not collected by Bazel either.
Pass one yourself if it is worth recording — `--metadata '{"collector":"shell"}'`.

For calls in tight loops, avoid `bazel run` in the inner loop — its
per-invocation configuration (and any differing `--action_env` flags on your
other builds) churns the analysis cache. Build once up-front and exec the
binary directly:

```bash
bazel build @evidence_store_bazel//cmd/evidence-bazel
BIN="$(bazel info workspace)/$(bazel cquery --output=files @evidence_store_bazel//cmd/evidence-bazel)"

for tgt in $targets; do
    "$BIN" record --procedure-ref "$tgt" --result PASS ...
done
```

<details>
<summary>Flags, and an example driving failure tests from a shell script</summary>

| Flag | Default | Description |
|------|---------|-------------|
| `--procedure-ref` | | Target label / test identifier (required) |
| `--result` | | `PASS`, `FAIL`, `ERROR`, or `SKIPPED` (required, case-insensitive) |
| `--evidence-type` | `ci` | How it was collected: `ci`, `manual_test` or `demonstration` |
| `--notes` | | Free-text stored under `metadata.notes` |
| `--tags` | | Comma-separated tags stored under `metadata.tags` |
| `--duration-ms` | | Duration in milliseconds (optional) |
| `--metadata` | | JSON object to merge into metadata (e.g. `'{"pattern":"static_assert"}'`) |
| `--invocation-id` | | Group multiple records from the same run |
| `--finished-at` | now (UTC) | RFC3339 timestamp |
| `--repo`, `--branch`, `--rcs-ref`, `--source` | auto-detected | Same as ingest path |
| `--api-url`, `--api-key` | `.evidence/config.yaml` | See below |
| `--dry-run` | `false` | Print the record as JSON instead of posting |

Config resolution order (highest priority first): command-line flag →
`.evidence/config.yaml` (searched upward from `BUILD_WORKSPACE_DIRECTORY` or
cwd). Environment variables are deliberately not consulted by `record`, so the
binary's behavior is stable across shell-env changes and Bazel's analysis cache
is not invalidated by invocations that happen to have different env.

```bash
INVOCATION_ID=$(uuidgen)
for tgt in $(discover_failure_tests); do
    if bazel build "$tgt" 2> stderr.log; then
        evidence-bazel record --procedure-ref "$tgt" --result FAIL \
            --notes "expected build failure but build succeeded" \
            --invocation-id "$INVOCATION_ID"
    elif grep -q "static_assert" stderr.log; then
        evidence-bazel record --procedure-ref "$tgt" --result PASS \
            --invocation-id "$INVOCATION_ID"
    else
        evidence-bazel record --procedure-ref "$tgt" --result ERROR \
            --notes "build failed without expected pattern" \
            --invocation-id "$INVOCATION_ID"
    fi
done
```

</details>

## Web interface

`docker compose up -d` serves the web UI from the same port as the API —
`http://localhost:8000`. It has four tabs: **Search**, **Analytics**, **Add
Result**, and **Access** (only shown to an administrator).

### Adding a manual test result

The **Add Result** tab is how a person files what they ran. **Repo**,
**Commit**, **Procedure** and **Result** are required; **Evidence type**
defaults to Manual Test.

- **Test log** — a markdown box with a live preview for the tester's account of
  what happened, stored as `metadata.observations`. Paste or drop an image into
  it and it uploads, is referenced from the log, and renders in the record
  dialog later; images over ~1.5 MB are downscaled in the browser first.
  Images are content-addressed (named by the SHA-256 of their bytes), so the
  same screenshot filed twice costs one object. Details:
  [Test logs](docs/api-reference.md#test-logs) and
  [Images in test logs](docs/api-reference.md#images-in-test-logs).
- **Locate**, next to the **Location** field, fills in this device's position
  to about a metre; typing over it makes the value the tester's own account,
  which is the point for a rig or track a GPS fix can't describe. Details:
  [Where a test was run](docs/api-reference.md#where-a-test-was-run).
- **Look up**, next to **Weather**, fetches conditions for the Location field
  (or this device's position) at the record's own **Finished at** time, from
  the server — never the browser — so a tester's coordinates are never handed
  to a third party. The line stays editable for the same reason the location
  does. Details: [Weather while a test ran](docs/api-reference.md#weather-while-a-test-ran).
- **Template** — save the current Repo/Branch/Commit/Procedure/Evidence
  type/Source/Tags as a named preset (stored in `localStorage`) and reapply it
  from the dropdown, for a bench that runs the same handful of manual
  procedures over and over.

### Searching evidence

The **Search** tab lists and filters records, with the same regex support the
API has: prefix a text filter with `~` for a POSIX regular expression instead
of an exact match, e.g. `~^release/` on branch. A single **Branch, tag or
commit** box matches whichever of the three the value turns out to be, instead
of asking which column to search. Selecting a row opens the record dialog, with
its test log, images, location and weather rendered alongside the record's
other fields rather than buried in a metadata dump. Full parameter list:
[Querying evidence](docs/api-reference.md#querying-evidence) and
[Regex filtering](docs/api-reference.md#regex-filtering).

### Inheriting test results between commits

When an impact analysis determines that a commit's evidence is still valid for
another — nothing changed in the code a test exercises — that's declared once,
rather than re-run:

```bash
curl -X POST http://localhost:8000/api/v1/inheritance \
  -H "Authorization: Bearer $ADMIN_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "repo": "myorg/firmware",
    "source_rcs_ref": "abc123def",
    "target_rcs_ref": "def456abc",
    "justification": "Impact analysis JIRA-1234: no changes in pkg/",
    "created_by": "ci-bot"
  }'
```

From then on, every record filed against `abc123def` (the tested version) also
appears when querying `def456abc` (the version that inherits it) — in the
**Search** tab, checking **Include inherited** (on by default) shows them in a
separate panel and badges the record dialog `Inherited`. There is currently no
form for creating a declaration in the web UI; `POST /api/v1/inheritance`
above, or `GET /api/v1/inheritance` to list existing ones, is the only way in
and needs the `admin` role. Full shape and the `include_inherited` query
parameter: [Creating an inheritance declaration](docs/api-reference.md#creating-an-inheritance-declaration).

### Analyzing test reliability

The **Analytics** tab aggregates evidence instead of listing it record by
record: an overview of the filtered window, a sortable per-test table (click a
header to rank by fail rate, flip rate, infra errors, or reliability), and
co-failure clusters with a minimal set of tests that would catch most failures.
It waits for **Apply** before querying, since these aggregations scan far more
than a search does. Selecting a row opens the matching records in Search;
**Export CSV** downloads exactly what the table is showing. Metrics, labels and
the clustering parameters are documented in full under
[Analytics](docs/api-reference.md#analytics).

### Managing access

The **Access** tab — visible only to a caller holding `principal:admin` — lists
every principal with its roles, when its key was last used, and whether it's
been revoked, and is where keys are issued, rotated and revoked. It requires
`EVIDENCE_AUTH_DB=true`; see
[Database-backed principals](#database-backed-principals) for the underlying
model and the equivalent `/api/v1/principals` calls.

## Contributing

### Build and test with Bazel

```bash
bazel build //...                    # build everything
bazel test //...                     # run all tests
bazel run //cmd/server               # start the server
```

`adapters/bazel` is a separate Bzlmod module (see [Bazel adapter](#bazel-adapter))
and is excluded from the root workspace via `.bazelignore`; build and test it
from inside that directory, the way CI does:

```bash
cd adapters/bazel
bazel test //...
```

Frontend unit tests (the web UI ships as plain ES modules, no build step) run
under Node's own test runner:

```bash
node --test web/tests/*_test.mjs
```

[prek](https://prek.j178.dev) hooks (`prek.toml`) run `go fmt`, `go vet`, a
build check, and basic hygiene checks (trailing whitespace, large files, merge
conflict markers) — install it and run `prek install` once per checkout so
these run on commit rather than in review.

### Run the smoke test

```bash
docker compose up -d
./scripts/smoke-test.sh
```

Exercises the running API end-to-end (`scripts/smoke-test.sh`) — useful after
a config or migration change to check the server actually comes up serving
correctly, beyond what the unit and integration tests cover.

### Ingest real test data

`./scripts/dogfood.sh` runs this repo's own `bazel test //...` and uploads the
results through the Bazel adapter — the fastest way to get evidence that looks
like a real CI run into a local store. See
[Using it on your own machine](#using-it-on-your-own-machine) for the adapter
commands it wraps.

### Seed demo data

`scripts/seed-demo` fills the database with synthetic evidence for demos and for
exercising the UI at realistic scale.

```bash
docker compose up -d db
go run ./scripts/seed-demo                       # 2,000,000 CI records + 3,000 manual tests
go run ./scripts/seed-demo --count 50000         # a smaller CI set
go run ./scripts/seed-demo --manual-tests 500    # fewer manual test logs
go run ./scripts/seed-demo --truncate            # replace existing evidence
```

It writes to Postgres with `COPY` rather than through the API — the batch
endpoint inserts one row per round trip, which takes tens of minutes at this
size. API-level validation is therefore bypassed, so the generator is written to
produce records that satisfy it anyway.

Records are clustered onto a limited set of repositories, branches and commits so
that filtering returns meaningful groups, with a realistic verdict distribution
(88% `PASS`) and timestamps biased towards the recent past. `--seed` makes a run
reproducible. Two million records occupy roughly 900 MB including indexes.

Manual tests are seeded separately from that bulk CI noise: `--manual-tests`
(3,000 by default) generates a curated batch of `manual_test` records with a
fixed 50/20/20/10 pass/skip/error/fail split, markdown logs of varying length,
and synthetically rendered screenshots (never real photos, so there is no
copyright question) written through the real content-addressed blob store. It
honours the same `EVIDENCE_BLOB_*` variables as `cmd/server` — set them to match
if you want the images visible through a server pointed at S3/MinIO rather than
the local `fs` default.

### Releasing the Bazel adapter

The adapter module (`evidence_store_bazel`) is published to the [Bazel Central Registry](https://registry.bazel.build/) so consumers can pin it via `bazel_dep` without a `git_override`.

**Release flow:**

1. Bump `version` in `adapters/bazel/MODULE.bazel`.
2. Commit and merge to `main`.
3. Tag the commit with `evidence_store_bazel-v<version>` (e.g., `evidence_store_bazel-v0.0.1`) and push the tag.
4. The `Release evidence_store_bazel` workflow (`.github/workflows/release-bazel-adapter.yml`) builds a source tarball of `adapters/bazel/`, creates a GitHub release, then calls the [`bazel-contrib/publish-to-bcr`](https://github.com/bazel-contrib/publish-to-bcr) reusable workflow to open a PR against [`bazelbuild/bazel-central-registry`](https://github.com/bazelbuild/bazel-central-registry) from the fork configured in the workflow.

**One-time setup (before the first release):**

- Fork `bazelbuild/bazel-central-registry` to `nesono/bazel-central-registry`.
- Create a classic Personal Access Token with `public_repo` scope and save it as the `BCR_PUBLISH_TOKEN` repository secret.
- BCR templates live in `.bcr/` at the repo root; `.bcr/config.yml` declares `adapters/bazel` as the module root.

### Where to look next

- [DESIGN.md](DESIGN.md) — overall architecture and data model
- [docs/rbac-design.md](docs/rbac-design.md) — principals, roles, permissions, SSO/SAML
- [docs/analytics-plan.md](docs/analytics-plan.md) — how the Analytics tab's metrics were designed
- [docs/retention-rules-plan.md](docs/retention-rules-plan.md) — retention rule syntax
- [docs/api-reference.md](docs/api-reference.md) — every HTTP endpoint, in full
