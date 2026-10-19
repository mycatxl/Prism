import { ApiError } from "./api-client";
import { ERROR_CODE_MESSAGES, GENERIC_ERROR_MESSAGE } from "../i18n/errors";

type TranslateFn = (text: string, options?: Record<string, unknown>) => string;

/** Anything CJK: a message that already reads as Chinese needs no lookup. */
const CJK = /[\u3400-\u9fff]/;

/**
 * What a failure says in the operator's language.
 *
 * The old version passed the backend's English `message` straight to `t()`, which
 * can only translate a dictionary key — so a real failure surfaced as machine
 * text, `CONFLICT: cannot delete Default platform`, in a Chinese console. Three
 * steps now, in order:
 *
 *   1. the error *code* maps to a sentence (`src/i18n/errors.ts`), because a code
 *      is closed, translatable and stable, while the message is free text;
 *   2. otherwise the message itself, if it already reads as Chinese — that is our
 *      own thrown message (`throw new Error(t("…"))`) and it goes through `t()` so
 *      an English session still gets English;
 *   3. otherwise a generic sentence: an English machine message is not a
 *      dictionary key, and showing it raw is exactly the defect being fixed.
 *
 * The `code: 说明` shape stays, because the code is what a support conversation
 * names even when the message has been translated.
 */
export function formatApiErrorMessage(error: unknown, t: TranslateFn): string {
  if (error instanceof ApiError) {
    const message = messageFor(error.code, error.message, t);
    return error.code ? `${error.code}: ${message}` : message;
  }
  if (error instanceof Error) {
    return messageFor("", error.message, t);
  }
  return t(GENERIC_ERROR_MESSAGE);
}

function messageFor(code: string, rawMessage: string, t: TranslateFn): string {
  const mapped = code ? ERROR_CODE_MESSAGES[code] : undefined;
  if (mapped) {
    return t(mapped);
  }
  const message = rawMessage.trim();
  if (!message) {
    return t(GENERIC_ERROR_MESSAGE);
  }
  if (CJK.test(message)) {
    // Our own sentence: it is a dictionary key, and `t()` is what makes it English
    // in an English session. A `zh-CN` session gets the same text back.
    return t(message);
  }
  return t(GENERIC_ERROR_MESSAGE);
}
