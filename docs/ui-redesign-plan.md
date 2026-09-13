# Desktop UI Redesign — Implementation Plan

Toward #161.

## Problem

The store looks like what it is: Pico's defaults with 1,823 lines of overrides
grown on top, one decision at a time. Nothing is wrong with any single rule; the
trouble is that there is no system, so every new component invents its own
spacing and type size, and changing anything safely means reading the whole
file.

Concretely, today:

| | |
|---|---|
| `web/static/style.css` | 1,823 lines, three media queries |
| Pico CSS | 83KB, vendored, overridden throughout |
| `font-size` declarations | two dozen distinct `em` values, chosen per component |
| Theme | `data-theme="light"` hardcoded; no dark mode |
| Specificity | rules fight — the phone work (#77) needed selectors spelled out at matching specificity to win against earlier ones |

A tester on a rig at night has no dark mode, and the stylesheet is at the size
where people stop editing it and start appending to it.

## Decisions taken

Answered before planning, because each one changes the work:

1. **Tailwind**, replacing Pico.
2. **A build step is acceptable.**
3. **Greenfield** design language — no house style to match.
4. **Tokens and consistency first**, not a redesign of every screen.
5. **Tailwind v4.** Its configuration lives in CSS (`@theme`) rather than a
   JavaScript config file, which suits a project that otherwise has no
   JavaScript tooling: the token set becomes a stylesheet, reviewable as one.
6. **A system font stack.** It costs no request and renders instantly, which
   matters most on the device this is least tested on — a phone at a proving
   ground with bad reception. Revisit only if it looks cheap.
7. **As dense as today, to start with.** The tightness is deliberate in a tool
   for reading evidence, and everything after the shell inherits whatever the
   shell picks. Loosening later is easy; discovering in phase 5 that every
   screen drifted roomier is not.

## The tension in those answers, and how this plan resolves it

Points 1 and 4 pull against each other. "Tokens first" suggests a low-risk pass
that leaves layouts alone; Tailwind means moving styling into the markup, which
touches **every element on every screen** — 115 class attributes across 544
lines of HTML, and 40 selectors that JavaScript reaches for by class name.

Done naively that is a full rewrite wearing a tokens hat, and the risk is
regressing a tool that works. So:

- Tailwind's **theme** is where the tokens live. Colours, spacing and type scale
  are defined once in `tailwind.config`, and that file *is* the design system —
  this is the part that unblocks #162 and #163.
- The migration is **screen by screen**, with Pico and Tailwind coexisting until
  the last screen is done. Each step is a PR that leaves the store working.
- Layouts are **preserved deliberately**. Where a layout is wrong it gets a note
  in #162/#163 rather than a fix here. The one exception is dark mode, which
  cannot be retrofitted without touching colour everywhere, and is the reason
  colour tokens come first.
- The 40 class names JavaScript depends on are treated as an **API**. They stay,
  as semantic hooks, whatever Tailwind classes sit beside them.

## Keeping what the frontend is good at

The property most worth protecting is that this frontend has no build step and
is therefore unusually testable: 287 tests run under `node --test` against the
files as served, with nothing installed.

**The build step is CSS-only.** Tailwind reads the markup and emits one
stylesheet; the ES modules are untouched, so the tests, the module-link test and
the `.mjs` syntax check all keep working exactly as they do now. A JavaScript
bundler is explicitly not part of this.

**The built CSS is committed.** That keeps `go build ./...`, `bazel build` and
the Docker image working with no node in sight — the Dockerfile is a Go builder
on Alpine and should stay that way. CI gains a check that the committed file
matches what the source produces, which is what stops it drifting.

Four places already know the stylesheet by name and all four need updating in
step: `index.html`, `web/static/sw.js`'s precache list, `web/BUILD.bazel`'s
`embedsrcs`, and the existing test that asserts those two lists agree.

## Phases

Each is a PR that leaves the store working and looking deliberate.

1. **The pipeline and the tokens.** `package.json` with a `build:css` script,
   the Tailwind config, the CI check, and the built stylesheet loaded *alongside*
   Pico. There is no Makefile in this repo and this plan does not add one —
   every other task here is invoked directly (`go test`, `node --test`,
   `bazel test`), and one npm script fits that.

   Nothing looks different yet. The deliverable is the token set: a palette
   including a dark variant, a type scale, a spacing scale, radii, and the
   two or three semantic colours this tool actually needs — pass, fail,
   inherited.
2. **The shell.** Header, navigation, tabs, footer, buttons, inputs. These are
   shared by every screen, so they set the vocabulary the rest reuse, and the
   result is visible immediately.
3. **Search.** The filter bar and the results table — the product's centre, and
   the densest thing in it. The `ch`-sized inputs get revisited here, since
   they are what overflows every narrow screen.
4. **Add Result.** The longest form, including the markdown editor's preview.
5. **Analytics and Admin.** Charts and the credential table.
6. **Remove Pico and `style.css`**, add dark mode as a first-class theme rather
   than a hardcoded attribute, and delete the phone rules that exist only to
   undo desktop assumptions.

   Split into three when it began, because the inventory showed more than this
   line assumed. `style.css` still held 93 top-level rules for components no
   screen phase owned — the datepicker, the outbox, the record-detail dialog,
   the advanced filters — and turning Pico off in the browser showed what it
   still carries: without it the header nav stacks vertically, Add Result's
   `.grid` collapses to one column, form fields shrink to their content, and
   every button nobody restyled goes flat.

   - **6a** moves those 93 rules onto tokens, with no visual change.
   - **6b** replaces what Pico provides — `nav`, `.container`, `.grid`, form
     field widths, `.secondary` and `.outline` buttons, `dialog`, `details` —
     and removes it. Done in two steps. First a base section in `app.css`
     reproduces the Pico rules that still matched anything, with Pico's
     values, checked by diffing the computed style of every rendered element
     in 15 UI states at five widths with Pico and without. Then those values
     move onto the tokens, which is the step that visibly changes anything.
   - **6c** makes dark mode real, and settles px against rem once this project
     controls the root font size.

## Not in scope

- **Layout changes.** Noted for #162 and #163 instead.
- **A JavaScript framework.** The module structure works and is tested.
- **Charts.** They are drawn, not styled, and reach only for colour tokens.
- **The Bazel adapter and CLI output**, which have no styling to speak of.

## Risks

**A tokens pass that quietly becomes a redesign.** The mitigation is the
screen-by-screen phasing and a rule that a layout change needs an issue, not a
commit.

**Dark mode revealing hardcoded colour.** Less of a risk than expected, and
worth recording because it was checked rather than assumed: there are four
inline `style` attributes in the markup and **no** colours set from JavaScript
at all. So the colour surface really is the stylesheet, and the four attributes
are cosmetic (`font-size` on header links).

**Layered utilities lose to unlayered rules.** Found in phase 1 and the single
most useful thing to know before phase 2: Tailwind emits into `@layer base`,
`components` and `utilities`, and unlayered CSS beats layered CSS in the cascade
regardless of load order. Pico and `style.css` are unlayered, which is why
adding the token sheet changed nothing — verified across eleven elements, every
computed value identical with it on and off.

It follows that a Tailwind utility will lose to any `style.css` rule touching the
same property. So each screen must have its old rules **deleted in the same
change that moves it over**; a half-migrated screen looks like the utilities
silently not working, and somebody will spend an afternoon on specificity before
finding this paragraph.

**Tailwind's defaults are not this tool's defaults.** Its spacing and type
scales are generous and this is a dense application. The theme has to say so in
phase 1, because every screen after the shell inherits it, and a drift towards
roomier is the kind of change nobody notices until all five screens have it.

**The committed-CSS check annoying somebody.** Forgetting to rebuild will fail
CI with a diff. That is the intended behaviour, and the failure should name the
command to run rather than leaving somebody to work it out.

## Open questions

None blocking. Two worth answering as the work reveals them:

1. **Where the theme file lives.** v4 puts tokens in CSS, so there is a choice
   between one stylesheet that both declares the theme and imports Tailwind, and
   a separate `theme.css` imported by it. The second reads better as a design
   system; the first is one fewer file to keep in three lists.
2. **Whether dark mode gets a toggle or follows the system.** Following
   `prefers-color-scheme` is free and right for most people; a toggle is what
   somebody on a night shift under bright rig lighting may actually want. Can be
   decided in phase 6.
