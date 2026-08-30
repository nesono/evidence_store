# SCIM 2.0 Provisioning — Implementation Plan

Closes #146.

## Problem

Single sign-on gets people *in*. Nothing gets them *out*.

A principal today is created the first time somebody logs in, and their roles
are reconciled from the group claims in that login's token. Both halves depend
on a login happening. So:

- **Somebody who leaves the company keeps their account.** Entra disables them,
  their next login would fail — but they have no next login. The principal stays
  enabled here, and so does any browser session they left open, until it expires
  on its own. Nothing tells this store they are gone.
- **A group change only lands when they next sign in.** Revoking `admin` by
  removing someone from a group is not revocation if they simply do not log in
  again; their open session keeps the role it was granted.
- **Nobody exists before their first login.** An administrator cannot grant a
  new joiner anything, or even see their name, until they have signed in once.

SCIM 2.0 is how Entra says all of this without waiting for the person. It is a
provisioning protocol, not an authentication one: Entra calls *us*, on a
schedule, to create, update, deactivate and delete users and groups. It
complements the OIDC login already built and replaces none of it.

## Requirements

From [Microsoft's SCIM guidance](https://learn.microsoft.com/en-us/entra/identity/app-provisioning/use-scim-to-provision-users-and-groups),
the parts Entra actually exercises:

| Requirement | Why it is not optional |
|---|---|
| `/scim/v2/Users` — POST, GET, PATCH, PUT, DELETE | Create, read back, deactivate, delete |
| `/scim/v2/Groups` — POST, GET, PATCH, PUT, DELETE | Group membership drives roles here |
| `GET /Users?filter=userName eq "x"` | How Entra checks whether it has already provisioned somebody |
| `GET /Users?filter=externalId eq "x"` | Same, matched the other way round |
| `PATCH` with `op: replace` on `active` | Entra's soft delete — this is the offboarding signal |
| `PATCH` with `op: add`/`remove` on `members` | How group membership changes arrive |
| `/scim/v2/ServiceProviderConfig`, `/ResourceTypes`, `/Schemas` | Entra reads these before it will provision anything |
| Bearer token auth | Entra holds one secret token and sends it on every call |
| `startIndex`/`count` pagination, `totalResults` | Entra pages through the directory on a full sync |

## Existing state

| Piece | Where | What it gives us |
|---|---|---|
| `principals` table | migration 000006, 000008 | `subject`, `kind`, `display_name`, `external_id`, `disabled_at` |
| `role_bindings` | migration 000006, 000008 | `role`, `scope`, `source` — `local` or `idp` |
| `UpsertFromIdP` | `internal/store/principal.go:208` | Matches on `external_id`, reconciles `idp` roles, leaves `local` ones alone |
| `SetDisabled` | `internal/store/principal.go:108` | Already the store's answer to "revoke this person" |
| `DeleteForPrincipal` | `internal/store/session.go:131` | Ends every session a principal has |
| `RolesForGroups` | `internal/auth/oidc.go` | Group name → role, via `EVIDENCE_GROUP_ROLE_MAP` |
| API key auth | `internal/auth/apikey.go`, `dbkey.go` | `Authorization: Bearer <key>`, resolved to a principal with permissions |
| Principals API | `internal/api/principals.go` | The Access tab: list, create, roles, disable/enable |

Three of these decide most of the design, and are worth stating plainly:

- **A disabled principal is kept, not deleted.** Evidence names its source, and a
  deleted principal would leave records attributed to nothing. SCIM's delete has
  to mean disable.
- **Roles already have a `source` column**, and login reconciliation only touches
  `idp` rows. SCIM needs the same treatment, and needs not to fight with it.
- **Bearer-token auth already exists** and already resolves to a principal with
  permissions, which is exactly the shape Entra's secret token wants.

## Design

### The identity problem, first

This is the part worth getting right, because everything else is mechanical.

A SCIM-provisioned user and the same human's later OIDC login must land on **one
principal**. If they do not, every person ends up as two rows: one that SCIM can
deactivate and one that actually holds their evidence.

They do not obviously match. The store keys a login on `external_id`, which is
`<issuer>|<sub>` — and Entra's `sub` is **pairwise**, unique to one application,
invented at first login. SCIM knows nothing about it. Entra's SCIM payload
carries `externalId`, which by default is mapped from `mailNickname` rather than
the directory object id, and is configurable per deployment. So neither end can
be relied on to hand over the same string.

**Proposal — claim on first login.** SCIM creates the principal with the identity
it does know (`userName`, the SCIM resource id, and `externalId`), and leaves
`external_id` null. The first OIDC login for a person whose subject matches an
unclaimed SCIM-provisioned principal *binds* to that row: it writes its
`<issuer>|<sub>` into `external_id` and from then on matches on it exactly as
today.

Binding is safe precisely because it is once-only and narrow. A row is claimable
only when it was created by SCIM and has never been claimed. After that the
login matches on `external_id` like any other, so the ordinary rename case — a
person changing their address — still follows the `sub`, and a second person
inheriting an old address cannot take over the first one's history.

This also means the answer does not depend on how a particular tenant maps
`externalId`, which is the part we cannot test without a tenant.

`EVIDENCE_OIDC_SUBJECT_CLAIM` is offered as a second route for deployments that
can emit the directory object id (`oid`) in both channels: set it, and the two
ends match directly with no claiming step. Default stays `sub`.

### Schema

Migration `000012_add_scim`:

```sql
ALTER TABLE principals ADD COLUMN scim_id TEXT;         -- the id we mint
ALTER TABLE principals ADD COLUMN scim_external_id TEXT; -- what Entra calls them
ALTER TABLE principals ADD COLUMN user_name TEXT;        -- SCIM userName
CREATE UNIQUE INDEX ... ON principals (scim_id) WHERE scim_id IS NOT NULL;

CREATE TABLE scim_groups (
    id           UUID PRIMARY KEY,
    scim_id      TEXT NOT NULL UNIQUE,
    external_id  TEXT,
    display_name TEXT NOT NULL,
    ...
);
CREATE TABLE scim_group_members (
    group_id     UUID REFERENCES scim_groups(id) ON DELETE CASCADE,
    principal_id UUID REFERENCES principals(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, principal_id)
);
```

Groups are stored rather than collapsed straight into roles because SCIM
requires them to be readable back: Entra does `GET /Groups?filter=displayName eq
"x"` and expects its own membership list returned. A group that maps to no role
still has to exist and still has to list its members.

### Roles from SCIM groups

Same `EVIDENCE_GROUP_ROLE_MAP` as the login path — one question, one answer, and
an operator should not have to state it twice.

Bindings are written with a **third** source, `scim`, alongside `local` and
`idp`. The alternative — reusing `idp` — makes the two paths clobber each other:
a login reconciles `idp` rows to the token's groups, which would delete
everything SCIM had just granted, and the next SCIM sync would put it back.
Effective permissions are the union of all three sources, which is what both
paths already assume.

An unmapped group grants nothing, exactly as at login.

### Deprovisioning

The point of the exercise.

| Entra sends | Store does |
|---|---|
| `PATCH` `active: false` | `SetDisabled(true)` **and** `DeleteForPrincipal` — the open browser dies with the account |
| `DELETE /Users/{id}` | The same. Never a row delete: evidence names its source |
| Member removed from group | Drop the `scim` binding for that group's role |
| Group deleted | Drop every `scim` binding it granted |

Disabling on deactivation is the whole feature: today an ex-employee's session
outlives their employment by up to `EVIDENCE_SESSION_TTL_HOURS`.

### Authentication

Entra holds one long-lived secret token. Rather than invent a second credential
system, the token **is an API key** — a principal of kind `api_key` holding a new
`scim:provision` permission and nothing else, created through the Access tab
like any other. So it is listed, rotatable and revocable with the machinery that
already exists, and it cannot read evidence.

A new role, `provisioner`, holds exactly that one permission. `admin` holds it
too.

`/scim/v2/*` is mounted outside `/api/v1` — it is a different protocol with its
own error format — but behind the same `Authenticate` middleware.

### What SCIM must *not* be able to do

- Not grant itself roles beyond the group map.
- Not disable the last enabled admin — the existing `CountOtherEnabledAdmins`
  guard applies, and a SCIM request that would is answered `403` rather than
  quietly locking everybody out.
- Not delete anything. `DELETE` disables.
- Not touch `local` role bindings. An administrator's grant survives a sync, the
  same promise the login path already makes.

## Not in scope

- **Filter language beyond `eq`** on `userName`, `externalId` and `displayName`.
  The full SCIM filter grammar is a parser and Entra sends nothing else.
- **`/Me`, bulk operations, sorting, ETags.** Optional in the spec; Entra does
  not require them. `ServiceProviderConfig` will say so honestly.
- **Password sync.** There are no passwords here.
- **SCIM as an outbound client.** This store is provisioned, it does not
  provision.

## Testing

No Entra tenant, so the same approach as the OIDC work: a mock in the test
process, driving the exact request shapes from Microsoft's documentation —
including the ones designed to trip a server up, like a `PATCH` whose path is
`members[value eq "x"]`.

Worth knowing: Microsoft publish a **SCIM validator** that runs a compliance
suite against a public endpoint, and Entra's provisioning UI has a *Test
Connection* button that exercises the discovery endpoints and a probe query.
Neither needs a tenant of our own to be useful, but both need this store to be
publicly reachable, so they are a manual step at the end rather than CI.

Keycloak does not speak SCIM natively, so `docker-compose.sso.yml` gains no new
service. Provisioning is tested against the mock and by hand with `curl`.

## Phases

Each is a PR that leaves the store working.

1. **Schema and the identity rule.** Migration, `scim_*` columns, the claim-on-
   first-login change to `UpsertFromIdP`, with tests for the claiming and — more
   importantly — for what must *not* be claimable.
2. **Users.** `/scim/v2/Users` with GET/POST/PUT/PATCH/DELETE, `eq` filtering,
   pagination, and the `active: false` path that disables and kills sessions.
3. **Groups and roles.** `/scim/v2/Groups`, membership patching, `scim` role
   bindings, and the union with `idp` and `local`.
4. **Discovery and auth.** `ServiceProviderConfig`, `ResourceTypes`, `Schemas`,
   the `provisioner` role and `scim:provision` permission.
5. **Documentation.** README section and an Entra walkthrough, alongside the
   existing Keycloak one.

## Open questions

1. **Should a SCIM-provisioned user who has never logged in appear in the Access
   tab?** They exist as principals, so they would by default. That seems right —
   it is the first time an administrator can see a joiner before their first day
   — but it will make the list considerably longer in a real directory.
2. **What should happen to evidence filed by someone SCIM later deletes?** The
   proposal keeps it and keeps the principal disabled. Worth confirming that
   matches the retention story.
3. **Should `active: false` also revoke API keys that person created?** They are
   separate principals with their own subjects, so today they would survive.
