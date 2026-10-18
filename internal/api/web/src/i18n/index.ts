import i18next from "i18next";
import { initReactI18next } from "react-i18next";
import { ensureEnglish } from "./english";
import {
  DEFAULT_LOCALE,
  detectInitialLocale,
  isEnglishLocale,
  normalizeLocale,
  persistLocale,
  setCurrentLocale,
  type AppLocale,
} from "./locale";
import { translateDocumentTitle } from "./title";

const initialLocale = detectInitialLocale();
setCurrentLocale(initialLocale);

/*
 * The console starts with an empty resource store on purpose. Chinese needs no
 * entries — the key is the sentence — and English arrives from `./english`, as a
 * chunk, only when a session reads it. `tests/i18n-coverage.test.mjs` is what
 * keeps that honest: every literal must exist in the English map, so "no
 * resource" can never mean "a Chinese string in English mode".
 */
const ready = i18next.use(initReactI18next).init({
  lng: initialLocale,
  fallbackLng: DEFAULT_LOCALE,
  supportedLngs: ["zh-CN", "en-US"],
  resources: {},
  interpolation: { escapeValue: false },
  keySeparator: false,
  nsSeparator: false,
  returnNull: false,
});

function applyDocument(locale: AppLocale) {
  setCurrentLocale(locale);
  persistLocale(locale);
  if (typeof document === "undefined") return;
  document.documentElement.lang = locale;
  document.title = translateDocumentTitle(locale);
}

i18next.on("languageChanged", (language) => {
  applyDocument(normalizeLocale(language));
});

if (typeof document !== "undefined") {
  document.documentElement.lang = initialLocale;
  document.title = translateDocumentTitle(initialLocale);
}

/**
 * Called before the first render, so an English session never paints Chinese keys
 * and then swaps them. A `zh-CN` session resolves without a network request.
 */
export async function primeLocale(): Promise<void> {
  await ready;
  if (isEnglishLocale(initialLocale)) await ensureEnglish();
}

export default i18next;
export { useI18n } from "./useI18n";
export { normalizeLocale };
