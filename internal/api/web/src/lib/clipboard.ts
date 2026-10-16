/**
 * Copies text to the clipboard from anywhere in the panel, and reports whether
 * it worked.
 *
 * `navigator.clipboard` only exists in a secure context, and this console is
 * routinely opened over plain HTTP on a LAN address. There `navigator.clipboard`
 * is `undefined`, so `navigator.clipboard.writeText(...)` threw a TypeError
 * *synchronously* — inside a `.then()` chain, which cannot catch it, so the
 * "copy failed" notice never ran and the page looked as if it had copied.
 * Call sites now branch on the resolved boolean instead.
 *
 * The legacy path is deliberately second: `execCommand("copy")` is deprecated,
 * but it is the only thing a non-secure context has. It is bracketed by a
 * try/catch because it throws when the document is not focused.
 */
export async function copyToClipboard(text: string): Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      return false;
    }
  }

  try {
    const area = document.createElement("textarea");
    area.value = text;
    area.setAttribute("readonly", "");
    area.style.position = "fixed";
    area.style.top = "0";
    area.style.opacity = "0";
    document.body.appendChild(area);
    area.select();
    const copied = document.execCommand("copy");
    document.body.removeChild(area);
    return copied;
  } catch {
    return false;
  }
}
