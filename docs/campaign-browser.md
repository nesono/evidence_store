# Campaign package browser

The **Campaigns** tab exposes [campaign import](campaign-import.md) in the web UI.
On a phone it lives under **More → Campaigns**. Opening `/#campaigns` selects it
directly. The tab requires `evidence:read`, matching the backend read endpoints.

## Import and browse

1. Build a `.tar.gz` using the [package tools](campaign-package-format.md).
2. Open **Campaigns**, choose the archive, and select **Import package**.
3. The server validates it and the page opens the imported package. Uploading
   identical bytes opens the existing package and reports that it was already
   imported. Failed uploads keep the file selected so the same bytes can be retried.
4. Browse imported packages with **Previous**, **Next**, and **Refresh**. The
   server's import-time order is preserved, 25 packages per page.
5. Select a package to open its **Campaign overview**: campaign identity,
   procedure count, and nominal planning context. Expand **Package source and
   import details** for the full digest, repository, and import attribution.
   A separate **Procedures** region contains a searchable list. Select a list
   item to open its **Procedure detail**, with its parent campaign named explicitly.
   Source identity is not a deployed
   software version or a build-provenance claim.

**Campaign key** is the source-authored `campaign.id` from the manifest; it may
be reused across updated packages. **Imported package UUID**, shown under
**Package source and import details**, identifies the server's imported record.
The **Package digest** identifies the exact archive bytes. Importing identical
bytes returns the same UUID; the digest is not a source commit hash.

The import form requires both `evidence:write` and `blob:write`; viewers can
browse definitions without being offered an upload they cannot perform. Files
over 16 MiB or empty files are rejected before upload; archive validation remains
the server's responsibility.

**Download entire campaign package** retrieves the exact imported archive through the
authenticated API. It requires `blob:read` in addition to the tab's read
permission. The download supports API-key and session authentication without
putting credentials in a link URL.

## Procedure preview

Opening a campaign shows a table of up to **10 procedures per page**, with
left-aligned **Title** and **Procedure reference** columns, in manifest
order, without automatically opening a procedure. Search filters the whole
campaign immediately as you type, matching title or procedure
reference; typing resets pagination to the first matching page. Use **Previous
procedures** and **Next procedures** to browse more matches. Counts show the
visible range and total matches. An empty search restores the full list.

Click a row, or activate its title button with the keyboard, to open its detail
below the table. Cells wrap on narrow screens. Changing the search or
procedure page closes the previous detail so it cannot be confused with the new
matches. **Close procedure detail** returns keyboard focus to its list item.
The selected procedure displays:

- Setup, steps, and criteria rendered with the existing safe Markdown renderer.
- Package-local setup images resolved through the validated asset map and fetched
  using the existing authenticated media helper.
- Disabled field/check previews, including labels, types, units, prescribed
  defaults, stable IDs, and result bindings.
- An expandable view of parameters, metadata defaults, and traceability.

The inspection is a **read-only definition preview**, visibly labeled as such.
Contributors can now choose **Start new attempt**, or resume a saved draft, to
open the separate [draft capture dialog](campaign-attempt-drafts.md). Its typed
inputs are editable and explicitly saved on the server. Drafts are not finalized
evidence and do not prefill the independent Add Result form.

## Connectivity

Package listing, opening, importing, downloading, and loading setup images require
a connection. The service worker caches the new UI modules as part of the app
shell but does not cache campaign API responses or prepare a package for offline
execution. An already opened definition can remain visible in the current page;
that is not an offline-readiness guarantee. Refresh errors are displayed alongside
the last loaded list, with an explicit retry instruction.

## Verification

```sh
node --test web/tests/*_test.mjs
npm run build:css
node --test web/smoke/campaigns_test.mjs
```

The campaign browser smoke test starts its own deterministic HTTP/API fixture and
drives a real Chrome using the existing smoke driver. It needs Chrome available
through `CHROME` or one of the driver's default locations, but no database or live
Evidence Store deployment. Run it separately from the live-server smoke suite.
It checks pagination, uploads, duplicate imports, validation and read errors,
late-response handling, filtering, setup-image requests, disabled controls,
viewer permissions, and phone navigation/layout.
