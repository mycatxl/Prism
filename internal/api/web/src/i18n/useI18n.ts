import { useCallback, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { ensureEnglish } from "./english";
import { isEnglishLocale, normalizeLocale, type AppLocale } from "./locale";

export type I18nContextValue = {
  locale: AppLocale;
  isEnglish: boolean;
  setLocale: (locale: AppLocale) => void;
  t: (text: string, options?: Record<string, unknown>) => string;
};

export function useI18n(): I18nContextValue {
  const { t, i18n } = useTranslation();

  const locale = normalizeLocale(i18n.resolvedLanguage ?? i18n.language);
  const setLocale = useCallback(
    (next: AppLocale) => {
      /*
       * English is a chunk, so the switch is ordered: fetch the map, then change
       * the language. Doing it the other way round repaints the tree in Chinese
       * keys first, and a language button that flashes the wrong language looks
       * broken even when the request succeeds.
       */
      const prepare = isEnglishLocale(next) ? ensureEnglish() : Promise.resolve();
      void prepare.then(() => i18n.changeLanguage(next));
    },
    [i18n],
  );

  return useMemo<I18nContextValue>(
    () => ({
      locale,
      isEnglish: isEnglishLocale(locale),
      setLocale,
      t: (text, options) => t(text, options),
    }),
    [locale, setLocale, t],
  );
}
