# A local identity provider

Keycloak, seeded with a realm, four users and three groups, so the single
sign-on path can be exercised on a laptop without a company directory behind
it. It is a development fixture: the passwords are the usernames and the client
secret is in the compose file.

## Before the first run

The browser and the app container have to reach Keycloak under the *same*
hostname. `go-oidc` fetches the discovery document and checks that the issuer
inside it matches the one configured, so the usual trick — `localhost` for the
browser, the service name for the container — makes the store refuse the
provider at startup. Giving the host the same name Docker uses settles it:

```
echo '127.0.0.1 keycloak' | sudo tee -a /etc/hosts
```

That is the only change outside the repository, and it is the only one needed.

## Running

```
docker compose -f docker-compose.yml -f docker-compose.sso.yml up -d --build
```

**Both files, every time.** `docker compose up` on its own recreates the app
from the base file, which has none of the single sign-on settings — the store
comes back up with its Log in button gone and Keycloak still running beside it,
looking for all the world like the stack is fine.

The store comes up on <http://localhost:8000> with a **Log in** button that
now goes to Keycloak instead of asking for an API key. Keycloak's own admin
console is on <http://keycloak:8080>, as `admin` / `admin`.

If something on the machine already holds 8080 — OrbStack and Docker Desktop
both like it — move Keycloak with one variable:

```
KEYCLOAK_PORT=8081 docker compose -f docker-compose.yml -f docker-compose.sso.yml up -d --build
```

Better, write it down once. Compose reads `.env` for substitution, so this
survives every later `up` and there is no variable to remember:

```
echo 'KEYCLOAK_PORT=8081' > .env
```

`.env` is git-ignored, because a port that happens to be free on one machine is
not a property of the project.

It is a single variable because the number appears in four places that must
agree: what Keycloak listens on, what it advertises, what is published to the
host, and what the app calls the issuer.

## Who is in the realm

Each user's password is their username.

| User    | Group                | Role in the store | What they can do            |
| ------- | -------------------- | ----------------- | --------------------------- |
| `alice` | `evidence-admins`    | `admin`           | everything                  |
| `bob`   | `evidence-engineers` | `contributor`     | file and read evidence      |
| `carol` | `evidence-viewers`   | `viewer`          | read only                   |
| `dan`   | `everyone-else`      | none              | signed in, and nothing more |

`dan` is the interesting one. His group is deliberately absent from
`EVIDENCE_GROUP_ROLE_MAP`, so he authenticates successfully and is granted
nothing at all — which is what pointing the store at a real corporate directory
must do to the thousands of people in it who have no business here.

Roles are worked out at login and written to `role_bindings`, so a group change
in Keycloak takes effect the next time that user signs in, not immediately.

## Logging out

**Log out** ends the session here *and* Keycloak's, so the next login asks for a
password again. The realm registers `http://localhost:8000/*` as a permitted
post-logout redirect, which Keycloak requires before it will send the browser
back; a store on a different address needs that changed to match.

Worth actually trying, because the failure it fixes is invisible: with only the
local session ended, clicking **Log in** is answered silently by Keycloak's
still-live session, and the store signs you back in so fast that the logout
button looks broken.

## Provisioning

Keycloak does not speak SCIM, so there is no provisioner in this stack by
default. To try the endpoints by hand, mint a key with the `provisioner` role in
the **Admin** tab and call them with it:

```
curl -H "Authorization: Bearer <the key>" http://localhost:8000/scim/v2/Users
```

That role reads nothing else, so the same key answers `403` on
`/api/v1/evidence` — which is the point of it.

## Pointing this at Microsoft Entra

There is a second overlay for that, `docker-compose.entra.yml`, and the realm
here is arranged to make the differences small. What follows was checked against
a real tenant rather than inferred (#152).

Register the store as an **app registration**, single tenant. Multi-tenant is
not merely discouraged: such a registration advertises its issuer as the literal
template `https://login.microsoftonline.com/{tenantid}/v2.0`, braces and all,
and strict issuer validation refuses it — correctly.

Put the tenant id, client id and secret in `.env`, along with the public address
Entra will send browsers back to. That address has to be reachable from the
internet and on real HTTPS, so on a laptop it means a tunnel:

```
cloudflared tunnel --url http://localhost:8000
```

Register `<public-url>/auth/callback` as a **Web** redirect URI, exactly — a
mismatch is the first error you will meet, and it names both URLs so it is easy
to fix.

**Use app roles, not groups.** Define app roles on the registration whose
*Value* is the string the role map keys on — `evidence-admins`,
`evidence-engineers`, `evidence-viewers` — and assign people to them under
Enterprise applications → Users and groups. Then
`EVIDENCE_OIDC_GROUPS_CLAIM=roles` and the map reads exactly as it does for
Keycloak's groups.

Emitting the group claim instead works, but a cloud-only tenant sends group
**object IDs** rather than names, so every key in the map becomes a GUID.

Assigning *groups* to app roles needs Entra ID P1; assigning individual users
does not, which is enough to try this out.

**Entra sends no `email` claim** for an account created in the portal, because
such an account has no mail attribute — whatever scopes are requested. The store
falls back to `preferred_username`, the UPN, so people are named
`user:alice@contoso.onmicrosoft.com` rather than by an opaque identifier. Set
real mail attributes and the address wins instead.

Leave `EVIDENCE_OIDC_PROVIDER_LOGOUT` off unless the machine is shared. With it
on, logging out of the store signs the person out of Entra entirely — every
other application that account opens — and Entra additionally asks which
identity they meant to abandon.

## Resetting

The realm is imported on first start and then owned by Keycloak's own storage.
To go back to what is checked in here:

```
docker compose -f docker-compose.yml -f docker-compose.sso.yml down -v
```

That drops the store's database too, and with it the principals created for
these users on their first login.
