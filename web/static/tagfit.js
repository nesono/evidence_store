// Fitting a record's tags onto one line of the results table (#167).
//
// The Tags column gets whatever width the fixed columns leave, and a row that
// grew to hold every tag would break the fixed-height window the table is read
// through. So a cell shows as many tags as fit, then "+N", and the cell's title
// lists them all. Which tags fit depends on rendered widths, so it is measured
// after the rows are drawn and again whenever the table changes width.

// How many of `widths` fit in `available`, keeping room for the "+N" counter
// whenever some are left out. The counter is measured for the number it will
// show, since "+10" is wider than "+9". The first tag always shows: a cell of
// nothing but "+3" says a record has tags without naming one, so an oversized
// first tag is shown truncated instead.
export function visibleTagCount(widths, available, { gap = 0, moreWidth = () => 0 } = {}) {
  const n = widths.length;
  if (n === 0) return 0;

  let span = 0;
  const spans = widths.map((w, i) => (span += w + (i ? gap : 0)));

  for (let k = n; k > 1; k--) {
    const counter = k < n ? gap + moreWidth(n - k) : 0;
    if (spans[k - 1] + counter <= available) return k;
  }
  return 1;
}

// Fit every tag cell under `root`. Reads every width before writing anything,
// so a window of five hundred rows costs one layout rather than one per cell.
export function fitTagCells(root) {
  if (!root) return;
  const lists = [...root.querySelectorAll("td.col-tags .tag-list")];
  if (lists.length === 0) return;

  // Show everything at its natural width while measuring. The class stops a
  // truncating first tag from shrinking to make room, which would measure a
  // row of tags that does not fit as one that does.
  root.classList.add("tags-measuring");
  for (const list of lists) {
    for (const chip of list.children) chip.hidden = chip.classList.contains("badge-more");
  }

  const available = lists[0].clientWidth;
  if (available === 0) {          // not laid out: a closed panel, a hidden tab
    root.classList.remove("tags-measuring");
    return;
  }
  const gap = parseFloat(getComputedStyle(lists[0]).columnGap) || 0;

  // One counter, measured once per digit count, stands in for every row's.
  const probe = lists[0].querySelector(".badge-more") || lists[0].appendChild(counterChip());
  const counterWidths = new Map();
  const moreWidth = hidden => {
    const digits = String(hidden).length;
    if (!counterWidths.has(digits)) {
      probe.hidden = false;
      probe.textContent = "+" + "8".repeat(digits);
      counterWidths.set(digits, probe.getBoundingClientRect().width);
      probe.hidden = true;
    }
    return counterWidths.get(digits);
  };

  const plans = lists.map(list => {
    const tags = [...list.querySelectorAll(".badge-tag")];
    const widths = tags.map(t => t.getBoundingClientRect().width);
    return { list, tags, shown: visibleTagCount(widths, available, { gap, moreWidth }) };
  });

  root.classList.remove("tags-measuring");
  for (const { list, tags, shown } of plans) {
    tags.forEach((tag, i) => { tag.hidden = i >= shown; });
    const hidden = tags.length - shown;
    let counter = list.querySelector(".badge-more");
    if (hidden === 0) {
      if (counter) counter.hidden = true;
      continue;
    }
    counter ||= list.appendChild(counterChip());
    counter.textContent = `+${hidden}`;
    counter.setAttribute("aria-label", `and ${hidden} more: ${tags.slice(shown).map(t => t.textContent).join(", ")}`);
    counter.hidden = false;
  }
}

function counterChip() {
  const chip = document.createElement("span");
  chip.className = "badge badge-more";
  chip.hidden = true;
  return chip;
}
