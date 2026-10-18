import type { AppLocale } from "./locale";

/*
 * The document title is the one string the browser chrome reads, so it cannot wait
 * for a dictionary. It ships in both languages here — a few dozen bytes — instead
 * of dragging the English map into the shell's first paint.
 */
const TITLES: Record<AppLocale, string> = {
  "zh-CN": "Prism · 节点工作台",
  "en-US": "Prism · Proxy Workbench",
};

export function translateDocumentTitle(locale: AppLocale): string {
  return TITLES[locale];
}
