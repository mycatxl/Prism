import i18next from "i18next";

/*
 * The English dictionary is fetched, never imported.
 *
 * Every key in this console is the Chinese sentence itself, so the Chinese
 * "dictionary" is the key — a `zh-CN` session can render without any resource at
 * all. English is the only language that has to be stored somewhere, and this
 * module is the only place that reaches for it.
 *
 * It lives in its own file rather than inside `./index` for two reasons: the hook
 * needs it, the boot needs it, and neither should import the other's module graph;
 * and it gives the bundler an obvious boundary, so the 150 kB map becomes a chunk
 * that a Chinese session never downloads.
 */
let english: Promise<void> | null = null;

/** Resolves once the English map is in i18next's store; a no-op after the first call. */
export function ensureEnglish(): Promise<void> {
  english ??= import("./translations").then((module) => {
    i18next.addResourceBundle("en-US", "translation", { ...module.EN_TRANSLATIONS }, true, true);
  });
  return english;
}
