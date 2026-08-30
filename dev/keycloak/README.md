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

The store comes up on <http://localhost:8000> with a **Log in** button that
now goes to Keycloak instead of asking for an API key. Keycloak's own admin
console is on <http://keycloak:8080>, as `admin` / `admin`.

If something on the machine already holds 8080 — OrbStack and Docker Desktop
both like it — move Keycloak with one variable:

```
KEYCLOAK_PORT=8081 docker compose -f docker-compose.yml -f docker-compose.sso.yml up -d --build
```

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

## Pointing this at Microsoft Entra

The realm is arranged to make the differences small. Registering the store as
an Entra app and swapping four values in the overlay is the whole change:

- `EVIDENCE_OIDC_ISSUER` becomes `https://login.microsoftonline.com/<tenant>/v2.0`
- `EVIDENCE_OIDC_CLIENT_ID` and `EVIDENCE_OIDC_CLIENT_SECRET` come from the app
  registration
- `EVIDENCE_OIDC_GROUPS_CLAIM` becomes `roles` if you map app roles, or stays
  `groups` if you emit the group claim — in which case the map keys are Entra
  *group object IDs*, not names, unless group names are configured to be emitted
- `EVIDENCE_COOKIE_SECURE` goes back to its default, since that deployment will
  be on HTTPS

## Resetting

The realm is imported on first start and then owned by Keycloak's own storage.
To go back to what is checked in here:

```
docker compose -f docker-compose.yml -f docker-compose.sso.yml down -v
```

That drops the store's database too, and with it the principals created for
these users on their first login.
