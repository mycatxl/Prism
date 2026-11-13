// Runs in Chromium through page.evaluate. Measure visible content rather than
// declared padding: a later selector, negative margin or wide child can defeat it.
export function measureCardInsets() {
  const surfaces = [
    ".workspace-card", ".workspace-surface-card", ".workspace-section",
    ".workspace-data-grid__cell", ".pricing-card", ".workspace-object-list",
    ".key-card-grid .workspace-object-row", ".workspace-command",
    ".workspace-advanced-filters", ".workspace-filter-toolbar", ".workspace-empty",
    ".workspace-table-region",
  ].join(", ");
  const controls = "button, input, select, textarea, [role='button']:not(.workspace-object-row), .workspace-status, .workspace-badge";
  const failures = [];
  let checked = 0;
  const visible = (el) => el.checkVisibility({ checkVisibilityCSS: true, checkOpacity: true });
  for (const surface of document.querySelectorAll(surfaces)) {
    if (!visible(surface)) continue;
    const style = getComputedStyle(surface);
    const border = surface.getBoundingClientRect();
    if (!border.width || ![style.borderLeftWidth, style.borderTopWidth, style.borderRightWidth, style.borderBottomWidth].some((value) => parseFloat(value) > 0)) continue;
    checked++;
    const inner = {
      left: border.left + parseFloat(style.borderLeftWidth),
      top: border.top + parseFloat(style.borderTopWidth),
      right: border.right - parseFloat(style.borderRightWidth),
      bottom: border.bottom - parseFloat(style.borderBottomWidth),
    };
    // A horizontal scroller reveals columns on demand. Check the table's true
    // first/last edges, while its containing card checks the scroll viewport.
    const table = surface.matches(".workspace-table-region") && surface.querySelector("table");
    if (table) {
      const tableBox = table.getBoundingClientRect();
      inner.left = tableBox.left;
      inner.right = tableBox.right;
    }
    const record = (rect, label) => {
      if (rect.width <= 0 || rect.height <= 0) return;
      const gaps = {
        left: rect.left - inner.left, right: inner.right - rect.right,
        top: rect.top - inner.top, bottom: inner.bottom - rect.bottom,
      };
      if (Object.values(gaps).some((gap) => gap < 15.5)) {
        failures.push({ surface: surface.className, content: label.slice(0, 70), gaps: Object.fromEntries(Object.entries(gaps).map(([edge, gap]) => [edge, Math.round(gap * 10) / 10])) });
      }
    };
    const inspect = (element, rect, label) => {
      if (!visible(element) || element.closest("svg")) return;
      let clipped = { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom };
      for (let ancestor = element; ancestor && ancestor !== surface; ancestor = ancestor.parentElement) {
        const css = getComputedStyle(ancestor);
        const box = ancestor.getBoundingClientRect();
        // Scrollable tables/code own their clipping. Their viewport must be
        // inset from the card; offscreen columns are not card overflows.
        if (/(auto|scroll)/.test(css.overflowX) && ancestor.scrollWidth > ancestor.clientWidth) {
          record(box, ancestor.className);
          return;
        }
        if (/(hidden|clip)/.test(css.overflowX)) {
          // Range includes the unpainted suffix of ellipsized text. Its visible
          // ink ends at the content box, including the ellipsis itself.
          const ellipsis = css.textOverflow === "ellipsis";
          clipped.left = Math.max(clipped.left, box.left + (ellipsis ? parseFloat(css.paddingLeft) + parseFloat(css.borderLeftWidth) : 0));
          clipped.right = Math.min(clipped.right, box.right - (ellipsis ? parseFloat(css.paddingRight) + parseFloat(css.borderRightWidth) : 0));
        }
        if (/(hidden|clip)/.test(css.overflowY)) {
          clipped.top = Math.max(clipped.top, box.top);
          clipped.bottom = Math.min(clipped.bottom, box.bottom);
        }
      }
      record({ ...clipped, width: clipped.right - clipped.left, height: clipped.bottom - clipped.top }, label);
    };
    for (const element of surface.querySelectorAll(controls)) {
      inspect(element, element.getBoundingClientRect(), element.getAttribute("aria-label") || element.textContent || element.tagName);
    }
    const walker = document.createTreeWalker(surface, NodeFilter.SHOW_TEXT);
    while (walker.nextNode()) {
      const node = walker.currentNode;
      if (!node.textContent.trim() || node.parentElement.closest(controls)) continue;
      const range = document.createRange();
      range.selectNodeContents(node);
      for (const rect of range.getClientRects()) inspect(node.parentElement, rect, node.textContent.trim());
    }
  }
  return { checked, failures: [...new Map(failures.map((failure) => [JSON.stringify(failure), failure])).values()] };
}
