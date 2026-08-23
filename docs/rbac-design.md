# RBAC — Design

Toward #15 (SSO/SAML logins).

## Problem

Companies evaluating the evidence store need to log in with their own identity
provider — Okta, JumpCloud, Keycloak, Entra, Google. That is issue #15. But an
SSO integration has nowhere to land today: authenticating a human tells the
server *who* is calling, and the server currently has no concept of a caller at
all, only of a shared secret that is or is not allowed to use verbs.

So this document designs the authorization half first. It defines principals,
roles, and permissions, and refactors the middleware so that authentication is a
pluggable front end producing a principal. SSO then becomes one more
implementation of that front end — a second `Authenticator` — rather than a
rewrite of every handler. Section 9 marks the slot it fills.

## Existing State

This section describes the tree as it stood when this document was written —
that is, before phase 1. Sections 1-6 and 8 are now implemented; see the phase
list at the end.

- `internal/auth/middleware.go` is the whole authorization system. It compares
  the bearer token against `config.APIKey` entries in constant time and puts a
  `Role` (`rw` or `ro`) in the request context.
- Authorization is **by HTTP method**: `isReadMethod` (`middleware.go:102`)
  treats `GET`/`HEAD`/`OPTIONS` as reads and everything else as a write. There
  is no per-route or per-resource distinction, so `POST /inheritance` — which
  `DESIGN.md:471` calls an elevated operation — is indistinguishable from
  posting a test result.
- `GetRole` (`middleware.go:27`) exists but no handler calls it. Nothing
  downstream of the middleware can tell one caller from another.
- Keys come from one env var parsed by `config.ParseAPIKeys`
  (`internal/config/config.go:180`) in `role:key` form. They are shared secrets,
  not identities: no name, no owner, no expiry, no revocation short of a
  redeploy.
- **If no keys are configured the middleware is a pass-through**
  (`middleware.go:49`) and the API is fully open. `TestNoKeysConfiguredAllowsAll`
  pins this. It is the default local-development posture and must survive.
- `internal/server/server.go:62-64` mounts `auth.Middleware` once for all of
  `/api/v1`, ahead of the rate limiter. `/healthz` (`server.go:36`) is routed
  outside that group and is deliberately unauthenticated; the SPA at `/*`
  (`server.go:89`) is served unauthenticated too.
- There are **no auth tables**. Migrations 000001-000005 are schema and index
  work only.
- `web/static/common.js:29` prompts the user for an API key and keeps it in
  localStorage; `apiFetch` attaches it and re-prompts on 401.
- Retention is a file-configured background worker (`cmd/server/main.go:65-84`,
  `internal/retention/worker.go`) with no HTTP surface, so despite
  `DESIGN.md:471` there is no retention endpoint to protect yet.
- `Evidence.Source` (`internal/model/evidence.go:113`) is a free-text string the
  client sets to whatever it likes. `DESIGN.md:469` wants human tokens pinned to
  their own username; nothing enforces that today.

## Design

### 1. Principal

Authentication produces a `Principal` — the caller's identity — which replaces
the bare `Role` in the request context.

```go
type Principal struct {
    ID          uuid.UUID // principals.id
    Subject     string    // "ci:nightly-build" or "user:alice@example.com"
    Kind        Kind      // KindAPIKey | KindUser
    DisplayName string
    Roles       []Role
    perms       permSet   // flattened from Roles at authentication time
}

func (p *Principal) Can(perm Permission) bool
func PrincipalFrom(ctx context.Context) (*Principal, bool)
```

Permissions are flattened once per request rather than recomputed per check.

*As implemented (phase 1):* `GetRole` was removed rather than kept as a shim.
It had no callers outside its own test, and the old `Role` type it returned
(`rw`/`ro`) is the name the four roles now need.

### 2. Permissions

A closed set of constants — a `Permission` string type, in the same spirit as
the closed `evidence_type` set from #90:

| Permission | Guards |
|---|---|
| `evidence:read` | `GET /evidence`, `/evidence/{id}`, `/evidence/distinct` |
| `evidence:write` | `POST /evidence`, `POST /evidence/batch` |
| `analytics:read` | `GET /analytics/*` |
| `blob:read` | `GET /blobs/{ref}` |
| `blob:write` | `POST /blobs` |
| `inheritance:read` | `GET /inheritance` |
| `inheritance:write` | `POST /inheritance` |
| `source:any` | Writing evidence whose `source` is not the caller's own subject |
| `principal:admin` | Managing principals and role bindings |
| `retention:admin` | Reserved — no endpoint yet; see Existing State |

`weather:read` is deliberately absent: `GET /weather`
(`server.go:79-81`) is a lookup that touches no stored evidence, and
`evidence:read` already covers anyone who could act on the answer.

### 3. Roles

Fixed roles, defined in code as static permission sets. Not runtime-composable —
the store has two roles today, and a role-CRUD API would be a larger surface than
the thing it governs.

| Role | Permissions |
|---|---|
| `viewer` | `evidence:read`, `analytics:read`, `blob:read`, `inheritance:read` |
| `contributor` | `viewer` + `evidence:write`, `blob:write` |
| `ci` | `contributor` + `source:any` |
| `admin` | `contributor` + `inheritance:write`, `principal:admin`, `retention:admin` |

`ci` is a distinct role rather than a flag because it is exactly the distinction
`DESIGN.md:469` draws: a build robot legitimately writes a `source` that is not
its own name (the build URL), a human should not.

Note `admin` does **not** subsume `ci`. An administrator who wants to backfill
evidence under someone else's `source` should hold both roles explicitly, so
that writing history in another party's name is always a deliberate grant.

### 4. Schema

```sql
-- migrations/000006_add_rbac.up.sql

CREATE TABLE principals (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subject      TEXT NOT NULL UNIQUE,  -- "ci:nightly" | "user:alice@example.com"
    kind         TEXT NOT NULL CHECK (kind IN ('api_key', 'user')),
    display_name TEXT NOT NULL DEFAULT '',
    -- Non-null only for kind='api_key'. Hex SHA-256 (amended in phase 2, below);
    -- the plaintext key is shown once at creation and never stored.
    key_hash     TEXT,
    disabled_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ,
    CHECK ((kind = 'api_key') = (key_hash IS NOT NULL))
);

CREATE TABLE role_bindings (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    principal_id UUID NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
    role         TEXT NOT NULL CHECK (role IN ('viewer','contributor','ci','admin')),
    -- Reserved for per-repo grants. '*' means store-wide, which is the only
    -- value written today; every authorization check asserts it.
    scope        TEXT NOT NULL DEFAULT '*',
    granted_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    granted_by   UUID REFERENCES principals(id),
    UNIQUE (principal_id, role, scope)
);

CREATE INDEX idx_role_bindings_principal ON role_bindings(principal_id);
```

*Amended in phase 2 — key hashing.* `key_hash` holds a hex SHA-256 of the token,
not Argon2id. A slow, salted hash earns its cost against a secret a human chose,
where the guessable space is small; these keys are minted by the server from 256
bits of `crypto/rand` and never supplied by a caller, so there is no space to
search and a dump yields nothing to either hash. What Argon2id would cost is
real: a salted hash cannot be looked up by value, so authentication would need a
key ID in the token and a second lookup, and every request would burn ~64 MiB
and tens of milliseconds on the hot path of a store sized for CI write volume.
As it stands, authenticating is one indexed equality check. This holds *only*
while keys are server-minted — an API letting a caller choose its own key would
put a guessable secret behind a fast hash and would have to bring Argon2id back
with it. Open question 3 is where that would surface.

The `scope` column is the one piece of forward-planning here. Permissions are
store-wide now, matching `DESIGN.md:470` ("all authenticated clients can read all
evidence"), but a trial with two companies on one deployment will want per-repo
isolation, and adding a column to a table with live grants is a worse migration
than carrying a defaulted one from the start. Until then the authorization check
asserts `scope = '*'` and rejects anything else, so a half-implemented scope
cannot silently widen access.

### 5. Authenticator

```go
type Authenticator interface {
    // Authenticate resolves a request to a principal. Returns
    // ErrNoCredentials if the request carries none for this scheme, so the
    // chain can try the next one.
    Authenticate(ctx context.Context, r *http.Request) (*Principal, error)
}
```

Two implementations at first:

- `StaticKeyAuthenticator` — wraps today's `config.APIKey` list. Preserves the
  constant-time compare and the no-keys-configured pass-through exactly.
- `DBKeyAuthenticator` — hashes the bearer token and looks up `principals`,
  honouring `disabled_at` and updating `last_seen_at`.

`OIDCAuthenticator` is the third, in section 9. A `Chain` tries each in order and
returns the first principal any of them resolves, which is what lets CI keys and
human SSO sessions coexist on the same endpoints.

*Amended in phase 2 — the chain defers rejection.* Phase 1's chain stopped at
the first authenticator that rejected a credential, on the reasoning that a
presented-but-wrong credential is an answer rather than an absence. That was
indistinguishable from correct while one scheme read the `Authorization` header.
It is wrong with two: an env-var key and a database key arrive identically, and
whichever authenticator is asked first has never heard of the other's keys, so
`StaticKeyAuthenticator` would reject every `DBKeyAuthenticator` key before it
was looked at. The chain now remembers a rejection, keeps looking, and returns
the remembered one only if nothing later resolves the caller. Nothing is
loosened — a credential no scheme accepts is still `401`, with the first
rejection's reason — and section 9 needs the same behaviour, where a stale
bearer token should not shadow a valid session cookie on the same request.

The one error that does stop the chain is `ErrAuthUnavailable`: the backend
could not be reached, so asking the remaining schemes could only turn "we cannot
check" into "your key is wrong". It surfaces as `503`, not `401`, so a database
outage does not send every pipeline in the building rotating credentials that
were fine.

### 6. Middleware split

Authentication and authorization become two middlewares:

```go
r.Route("/api/v1", func(r chi.Router) {
    r.Use(auth.Authenticate(authenticator))  // identity only
    r.Use(ratelimit.Middleware(cfg.RateLimit))

    r.With(auth.Require(auth.PermEvidenceRead)).Get("/evidence", evidenceAPI.List)
    r.With(auth.Require(auth.PermEvidenceWrite)).Post("/evidence", evidenceAPI.Create)
    ...
    r.With(auth.Require(auth.PermInheritanceWrite)).Post("/inheritance", inheritanceAPI.Create)
})
```

This is where the method-based rule dies: `POST /inheritance` now requires
`inheritance:write`, which only `admin` holds, delivering the elevated-role
requirement `DESIGN.md:471` has always specified.

Rate limiting stays *after* authentication so buckets can eventually key on
principal rather than IP, and unauthenticated floods are still cheap to reject.

### 7. Binding `source` to identity

`source:any` is enforced in `EvidenceHandler.Create`/`CreateBatch`, not in
middleware — only the handler has parsed the body and can see the field.

- Caller has `source:any` (i.e. `ci`): `source` is accepted as sent.
- Otherwise: `source` must equal the principal's subject. Empty is filled in
  with the subject; a mismatch is `403`. In a batch, one bad row rejects the
  batch, consistent with how batch validation already behaves.
- No principal at all (keys not configured, local dev): unchanged, anything goes.

This is the rule `DESIGN.md:469` describes, and it is what makes `source`
trustworthy enough to attribute a manual test result to a person — which is
exactly what the just-merged #89/#94 fix was about.

### 8. Configuration and compatibility

The upgrade must not lock anyone out. Three postures, in precedence order:

1. **No keys, no SSO** — pass-through, API open. Unchanged; still the default.
2. **`EVIDENCE_API_KEYS` set** (today's env var) — keys keep working. `rw` maps
   to `ci` *and* `admin`, `ro` maps to `viewer`. `rw` includes `ci` rather than
   just `contributor` because today's `rw` keys are overwhelmingly CI writers
   that set their own `source`, and mapping them to `contributor` would start
   rejecting writes that worked yesterday.

   *Amended in phase 1:* `rw` also carries `admin`. An `rw` key can post an
   inheritance declaration today, and section 6 moves that behind
   `inheritance:write`, which only `admin` holds — so `ci` alone would lock out
   exactly the operators this posture exists to protect. An env var full of
   store-wide shared secrets is an administrator's credential either way. The
   finer split is available from phase 2 on, where roles are granted per
   principal.
3. **`EVIDENCE_AUTH_DB=true`** — `principals` becomes authoritative; a bootstrap
   admin is seeded on first start from `EVIDENCE_BOOTSTRAP_ADMIN` and its
   one-time key is logged.

Posture 2 is the compatibility bridge and should stay supported for at least one
release after 3 exists.

*Clarified in phase 2:* postures 2 and 3 are not exclusive. With both set, both
key sources are live and the env list is checked first, because it costs no
round trip. That is the migration: issue database keys, move pipelines over one
at a time, clear `EVIDENCE_API_KEYS` when the last has moved. What posture 3
does make authoritative is the *closed* door — the DB authenticator is never
`ErrAuthDisabled`, so an empty `principals` table means nobody may in rather
than everybody. That is also why the bootstrap admin exists: the API for issuing
the first key is itself behind `principal:admin`, so without it a fresh database
would have no way in at all.

### 9. The SSO slot — what fills in later

This design is deliberately unfinished at exactly one point: `OIDCAuthenticator`.
Landing #15 on top of it means:

- **Login flow.** Authorization Code + PKCE. `GET /auth/login` redirects to the
  IdP, `GET /auth/callback` validates the ID token, upserts a
  `kind='user'` principal keyed by the IdP `sub` claim, and sets an
  `HttpOnly; Secure; SameSite=Lax` session cookie. `Authenticate` accepts the
  cookie alongside bearer tokens.
- **Group mapping.** A config map from IdP group claim to role, e.g.
  `EVIDENCE_OIDC_ROLE_MAP="eng-all:contributor,eng-leads:admin"`. On each login
  the user's derived bindings are reconciled to match their current claims,
  while bindings granted locally are left alone — so an IdP that exposes no
  useful groups still lets an admin grant roles by hand. This is why role
  assignments live in the DB rather than being read straight off the token.
- **SAML.** Same shape, different front end: an `Assertion Consumer Service`
  endpoint replaces the OIDC callback and attribute statements replace claims.
  Everything from `Principal` inward is unchanged. That is the point of doing
  RBAC first.
- **Web UI.** `web/static/common.js` stops `prompt()`ing: a 401 redirects to
  `/auth/login`, and `apiFetch` relies on the session cookie (with a CSRF token
  on writes, since cookies are ambient where bearer headers were not). The
  API-key path stays for CI and scripts.

## Implementation Phases

Each phase is independently shippable and leaves the tree green.

1. **`Principal` and permissions.** ✅ Landed. Introduces the types, the role
   table, and `Require`. Backed by the existing static keys via
   `StaticKeyAuthenticator` and the posture-2 mapping. Routes converted from
   method-based to permission-based. No migration. *This phase alone delivers
   the elevated role for `POST /inheritance`.*
2. **Migration 000006 + `DBKeyAuthenticator`.** ✅ Landed. Principals and
   bindings in Postgres, key minting and hashing, bootstrap admin. *This phase
   is what makes the four roles individually grantable, and a key revocable
   without a redeploy.*
3. **`source` binding.** Enforce section 7.
4. **Principal admin API + UI.** `/api/v1/principals` CRUD behind
   `principal:admin`; a UI tab for issuing and revoking keys.
5. **OIDC** (#15 proper), then SAML.

Phases 1-3 are the RBAC foundation this document is for; 4 and 5 are what it
was built to carry.

## Testing

Tests are written with each phase, not after (repo convention, and the existing
auth tests are a good model to extend):

- `internal/auth/middleware_test.go` — extend the existing table tests: each role
  against each permission, `Require` returning 401 unauthenticated vs 403
  unauthorized, and the `ErrNoCredentials` chain order.
- `tests/auth_integration_test.go` — the compatibility cases are the important
  ones. `TestNoKeysConfiguredAllowsAll` must keep passing untouched;
  `TestROKeyCanGetButNotPost` and `TestRWKeyCanPost` must keep passing under the
  posture-2 mapping. Add: `ro` key blocked from `POST /inheritance`, `rw`/`ci`
  key allowed, and a `contributor` blocked from it.
- New `tests/rbac_integration_test.go` — principal lookup against a real
  Postgres via the existing testcontainer harness, disabled-principal rejection,
  and the `source` binding rules including the batch case.
- `migrations/000006_add_rbac.down.sql` exercised by the existing
  `tests/migration_test.go` round-trip.

## Open Questions

1. **Session storage for SSO.** Stateless signed cookie (simple, but revocation
   waits for expiry) versus a `sessions` table (immediate revocation, one more
   read per request). Deferrable to phase 5, but it decides whether "log this
   user out now" is possible.
2. **Per-repo scoping trigger.** The `scope` column is reserved but inert. Worth
   confirming whether multi-company trials will share one deployment — if they
   will, scoping is phase 4 rather than someday, and it touches
   `internal/store` query paths broadly.
3. **API keys owned by users.** Should a human be able to mint a personal key
   that inherits their roles (convenient for workstation adapters, and the
   natural home for `DESIGN.md`'s developer-workstation case), or do keys stay
   separate principals administered centrally?
