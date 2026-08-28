# Lightdash — Assessment

Assesses #112: would Lightdash serve better than the Search and Analytics tabs
this store builds itself, and what would moving cost?

**Recommendation: do not migrate. Add it beside the store if ad-hoc slicing is
the actual need.** The reasoning is below, and the one finding that decides it
on its own is [access control](#every-lightdash-user-would-see-every-record).

## What Lightdash is, as of August 2026

Checked rather than remembered, because the answer to one of these has changed
and it is the one everybody quotes:

| | |
|---|---|
| Licence | Core is MIT and self-hostable for free. The `ee/` directory and the AI agent features are under a separate licence |
| **dbt required?** | **No longer.** Warehouse-first onboarding creates a project with dbt type `NONE`, and explores are populated from the warehouse catalogue instead of dbt models. A dbt project can be added later |
| Postgres | Supported directly as a warehouse |
| Self-hosting | Needs its own external Postgres instance for application state, separate from the warehouse it reads |
| Embedding | **Cloud and Enterprise On-Prem only.** Not available in the free self-hosted edition |

The dbt point matters: "Lightdash means adopting dbt" was the obvious reason to
stop reading, and it is no longer true. So this assessment turns on other
things.

## What the store's Search and Analytics actually are

Worth being precise, because "replace our analytics with a BI tool" quietly
assumes the tabs are analytics. Two of the three are not.

**Search** is record retrieval: regex filters on refs and notes, cursor and
offset paging, deep-linkable URL state, distinct-value suggestions. The thing it
opens — the record dialog — is the point of the whole store: a markdown test log
with images fetched through `/api/v1/blobs` under the caller's own credential,
a location with its accuracy shown only when the fix was the device's, a weather
line with its observed hour shown only when the reading was the service's.

**Add Result** is collection, including the offline campaign path from #74: the
app shell, the outbox, photographs named on the device, weather written down
where there is nobody to ask.

**Analytics** is the only BI-shaped surface, and about half of it is not BI:

| Piece | Where it lives | Would it port? |
|---|---|---|
| Counts per test by verdict, first/last seen | SQL (`internal/store/analytics.go`) | Yes — ordinary aggregation |
| Flip rate, flaky-commit disagreements | SQL window functions | Yes |
| Wilson lower bound, threshold labels | Go (`internal/analytics/metrics.go`, `stats.go`) | As arithmetic in SQL, awkwardly |
| **Co-failure clusters and the minimal covering set** | Go (`internal/analytics/cluster.go`, 304 lines) | **No** |

That last row is the analytics feature [the plan](analytics-plan.md) called the
most valuable — choose a subset of tests that covers most failures with fewer
runs. It is a Jaccard similarity matrix and a greedy set cover. It is not an
aggregate, so it is not a chart, and no BI tool computes it. It would stay in Go
and Lightdash could at best display a table it was handed.

## Where Lightdash would genuinely help

Real gaps, and they are real:

- **Questions the fixed tabs cannot ask.** Failures by hardware generation over
  time, pass rate by branch by month, anything grouping on a `metadata` field.
  Today that needs a code change; there it is a dropdown.
- **Dashboards for people who do not read the store.** A quality lead wanting a
  weekly picture is badly served by a table of records.
- **Scheduled delivery.** Charts pushed to Slack or email on a schedule; the
  store has nothing like it and would not be a good place to build it.
- **Not building more chart code.** `web/static/analytics.js` is 586 lines and
  every new view is more.

## Where it would not

### Every Lightdash user would see every record

Lightdash connects to Postgres with one warehouse credential. Everything it
shows is what that credential can read, so any Lightdash user sees all evidence,
regardless of the principal, role and source binding this store enforces
(DESIGN.md §8, [docs/rbac-design.md](rbac-design.md)). Embedding — which is how
you would ride the store's own session — is not in the free edition.

So the free path is a second login with a second, coarser access model. For an
internal engineering store that may be acceptable; it should be a decision
somebody makes deliberately rather than one that arrives with a docker-compose
service.

### The schemaless half becomes schema again

`metadata` is JSONB on purpose so new fields need no migration (DESIGN.md §4.1,
§4.3). Warehouse-first onboarding reads the catalogue, which shows one `jsonb`
column. To chart on `target_hw_type` or `hw_generation` you write views
flattening the fields you care about, and you update them whenever a new field
starts mattering — which is precisely the migration burden the JSONB design was
chosen to avoid. Not fatal, but it is a cost that lands on the same people who
add the fields.

### Search, the record dialog and offline are not replaceable

None of them are BI, and all of them are the product. Whatever Lightdash does,
those tabs stay. "Migration" is therefore the wrong frame: the most that could
be removed is the Analytics tab, and only the half of it that is aggregation.

## What it would cost

| Work | Estimate |
|---|---|
| Deploy Lightdash and its own Postgres; TLS, backups, upgrades | ~1 day, then ongoing |
| Read-only role, and a replica so BI queries do not compete with ingest | ~0.5 day |
| Views flattening the `metadata` fields worth charting | 1–2 days, recurring as fields are added |
| Explores, dimensions and metrics defined in Lightdash | 1–2 days |
| Reproduce the labelled metrics as SQL (Wilson, thresholds) | 2–3 days |
| Co-failure clustering | Does not port |
| Decide the auth story | Unbounded; the free answer is an access-control regression |

**Roughly one to two weeks** for a useful BI surface *alongside* the store. The
migration — retiring what exists — is not reachable at any price, because
clustering, search, the record dialog and offline collection are not things a BI
tool does.

Against that, what would be retired: an aggregation query, ~300 lines of Go
metrics, and part of a 586-line JS view. The clustering, the store's own
retrieval and the offline path all stay either way.

## Recommendation

**Keep the store's Search and Analytics. If ad-hoc slicing and dashboards are
what people are actually asking for, add a BI tool pointed at a read-only
replica and leave the UI alone.** That gets the real benefit — arbitrary
group-bys, dashboards, scheduled charts — without touching anything that works,
and it can be undone by deleting a service.

If that is the plan, Lightdash is a reasonable choice now that dbt is optional,
and its semantic layer is worth having if metric definitions are going to be
shared. Metabase or Superset connect to Postgres with less ceremony and no
semantic layer; the gap has narrowed enough that the choice should be made on
whether anyone wants to maintain metric definitions, not on setup cost.

## The cheap way to find out

Before any of the above, a day's spike answers the question this document can
only reason about:

1. Bring Lightdash up in `docker-compose` against a copy of the demo data —
   `scripts/seed-demo` generates a few hundred thousand rows.
2. Point it at `evidence` with warehouse-first onboarding. No dbt, no views.
3. Try to answer the five questions people actually ask, in front of the people
   who ask them.

If the raw table plus a couple of views answers them, the rest of the cost above
is real and the benefit is proven. If it does not, nothing was spent.
