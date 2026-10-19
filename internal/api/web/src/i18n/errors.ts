/**
 * 后端错误码 → 中文句子。
 *
 * The API answers with English machine messages (`cannot delete Default
 * platform`, `alert_window_minutes: must be at least 1`). Those are not
 * dictionary keys, so passing one to `t()` returned it unchanged and a Chinese
 * session showed English — code and all, as `CONFLICT: cannot delete Default
 * platform`.
 *
 * A code carries no free text, so it is the one thing that can be mapped before
 * the message is tried: the sentence below is a dictionary key in its own right
 * (`src/i18n/translations.ts`), which is what makes it render in both locales.
 * The list is closed on purpose — the codes are the ones `internal/api/errors.go`
 * actually writes (`writeServiceError` plus `PAYLOAD_TOO_LARGE`), so an unknown
 * code falls through to the message instead of inventing a sentence for a
 * failure nobody described.
 */
export const ERROR_CODE_MESSAGES: Readonly<Record<string, string>> = {
  INVALID_ARGUMENT: "请求参数有误，请检查后重试",
  NOT_FOUND: "目标不存在，可能已被删除",
  CONFLICT: "操作与当前状态冲突，请刷新后重试",
  FORBIDDEN: "当前账号无权执行该操作",
  INTERNAL: "服务器内部错误，请稍后重试",
  RATE_LIMITED: "操作过于频繁，请稍后重试",
  REGISTRATION_CLOSED: "当前部署已关闭注册",
  UNAUTHORIZED: "登录状态已失效，请重新登录",
  PAYLOAD_TOO_LARGE: "提交的数据过大，请缩减后重试",
};

/** 兜底句子：既没有可映射的错误码，消息本身也不是词条时使用。 */
export const GENERIC_ERROR_MESSAGE = "操作失败，请稍后重试";
