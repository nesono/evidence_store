// The phone's navigation: a bar at the bottom of the screen and a sheet for
// what does not fit in it (#162).
//
// On a phone the tester is standing next to the rig filing what just happened,
// often one-handed, so the tabs go where a thumb reaches and capture comes
// first. Nothing here duplicates what the header already does. A tab in the
// bar clicks the header's own tab, so permissions, greying out and the #add
// shortcut behave exactly as on a desktop; the More sheet mirrors the header's
// sign-in, install and status items and clicks them in turn.

import { openTabFromHash } from "./offline.js";

// The one definition of "a phone" in JavaScript. app.css states the same
// width for the layout; the two have to agree.
export const PHONE_QUERY = "(max-width: 767px)";

// phoneStartTab decides where a phone opens: Add Result, unless the address
// already says where to go. A tab in the fragment is a shortcut, and search
// filters or a record in the query are a link somebody sent — both win.
export function phoneStartTab({ hashTab = null, hasLinkState = false, isPhone = false } = {}) {
  if (!isPhone || hashTab || hasLinkState) return null;
  return "add";
}

// Tabs that live in the More sheet rather than the bar. When one is showing,
// More is the bar's current item.
const SHEET_TABS = new Set(["analytics", "access"]);

export function mountPhoneNav({ openOutbox }) {
  const bar = document.getElementById("phone-nav");
  const sheet = document.getElementById("more-sheet");
  if (!bar || !sheet) return;

  const headerTab = name => document.querySelector(`.nav-tab[data-tab="${name}"]`);

  // Selecting through the header tab rather than beside it: openTabFromHash
  // refuses a tab that is hidden or greyed out, which is the rule the header
  // already enforces.
  const select = name => openTabFromHash(`#${name}`);

  for (const button of bar.querySelectorAll("[data-tab]")) {
    button.addEventListener("click", () => select(button.dataset.tab));
  }
  bar.querySelector("[data-phone=outbox]")?.addEventListener("click", () => openOutbox());
  bar.querySelector("[data-phone=more]")?.addEventListener("click", () => {
    refreshSheet(sheet);
    sheet.showModal();
  });
  document.getElementById("close-more")?.addEventListener("click", () => sheet.close());

  for (const button of sheet.querySelectorAll("[data-tab]")) {
    button.addEventListener("click", () => {
      sheet.close();
      select(button.dataset.tab);
    });
  }
  for (const button of sheet.querySelectorAll("[data-proxy]")) {
    button.addEventListener("click", () => {
      sheet.close();
      document.getElementById(button.dataset.proxy)?.click();
    });
  }

  // Which tab is current, and which the caller may not use, follow the header
  // however it changes: a click, a shortcut, the start tab below.
  const sync = () => {
    const active = document.querySelector(".nav-tab.active")?.dataset.tab;
    for (const button of bar.querySelectorAll("[data-tab]")) {
      const tab = headerTab(button.dataset.tab);
      button.classList.toggle("active", button.dataset.tab === active);
      button.classList.toggle("unavailable", !!tab?.classList.contains("unavailable"));
      button.title = tab?.closest("li")?.title || "";
      if (button.dataset.tab === active) button.setAttribute("aria-current", "page");
      else button.removeAttribute("aria-current");
    }
    const more = bar.querySelector("[data-phone=more]");
    more?.classList.toggle("active", SHEET_TABS.has(active));
  };
  new MutationObserver(sync).observe(document.querySelector("body > header nav"), {
    subtree: true, attributes: true, attributeFilter: ["class", "hidden", "title"],
  });
  sync();
}

// The sheet shows what the header would show: the same items, visible or not
// for the same reasons, under the same words.
function refreshSheet(sheet) {
  const concealed = el => !el || el.hidden || !!el.closest("[hidden]");

  for (const slot of sheet.querySelectorAll("[data-mirror]")) {
    const source = document.getElementById(slot.dataset.mirror);
    slot.hidden = concealed(source) || !source.textContent.trim();
    // Copied as markup: both sources are built from escaped text and a
    // status dot, and the dot is part of what they say.
    slot.innerHTML = slot.hidden ? "" : source.innerHTML;
  }
  for (const button of sheet.querySelectorAll("[data-tab]")) {
    const tab = document.querySelector(`.nav-tab[data-tab="${button.dataset.tab}"]`);
    button.closest("li").hidden = concealed(tab);
    button.disabled = !!tab?.classList.contains("unavailable");
  }
  for (const button of sheet.querySelectorAll("[data-proxy]")) {
    const source = document.getElementById(button.dataset.proxy);
    button.closest("li").hidden = concealed(source);
    if (button.hasAttribute("data-mirror-text") && source) button.textContent = source.textContent;
  }
}
