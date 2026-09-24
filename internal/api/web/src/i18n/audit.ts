// English copy for the audit log feature. Chinese UI strings are the keys, so
// anything missing here shows Chinese text in the en-US locale.
export const AUDIT_TRANSLATIONS: Record<string, string> = {
  审计日志: "Audit Logs",
  "记录管理员对配置的写操作，仅成功的写操作会被记录（保留 90 天，最多 100000 条）。":
    "Administrator configuration writes are recorded here. Only successful writes are logged, kept for 90 days and capped at 100000 rows.",
  动作: "Action",
  变更字段: "Changed fields",
  结果: "Result",
  操作者: "Actor",
  来源: "Source",
  "仅记录成功的写操作": "Only successful writes are recorded",
  "管理员令牌的指纹前缀（不含令牌本身）":
    "Fingerprint prefix of the administrator token; the token itself is never stored",
  "请求的客户端地址": "Client address of the request",
  暂无审计记录: "No audit records",
};
