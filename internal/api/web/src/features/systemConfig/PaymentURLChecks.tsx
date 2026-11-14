import { useI18n } from "../../i18n";
import type { PaymentURLCheck } from "./types";

const messages: Record<string, string> = {
  reachable: "网址可以访问；启用支付前仍需验证一次真实回调。",
  derived: "未配置网址，将使用付款时的访问域名；请确认该域名公开可达。",
  non_public: "网址没有可供支付平台访问的公开地址，请检查域名和 DNS。",
  dns_failed: "网址域名无法解析，请检查 DNS 配置。",
  connection_failed: "无法建立经过验证的 HTTPS 连接，请检查证书、网络和防火墙。",
  not_found: "网址返回 HTTP 404，请检查回调路径。",
  access_denied: "网址被访问控制拒绝，请允许支付平台访问公开回调。",
  redirect: "网址发生重定向，请直接填写最终 HTTPS 地址。",
  unexpected_path: "网址可达，但路径不是 Prism 的付款通知入口；请确认转发规则。",
  method_or_signature: "服务器要求有效签名或特定请求方法，请继续验证真实支付回调。",
  http_error: "服务器未正常响应，请检查回调服务。",
  invalid_url: "网址格式无效，请填写完整 HTTPS 地址。",
};

export function PaymentURLChecks({ checks }: { checks: PaymentURLCheck[] }) {
  const { t } = useI18n();
  if (checks.length === 0) return null;
  return <div className="workspace-stack" aria-label={t("付款网址检查结果")}>
    {checks.map((check) => <p key={check.field} className={`workspace-notice ${check.status === "reachable" ? "" : "workspace-notice--warn"}`} role={check.status === "reachable" ? "status" : "alert"}><strong>{t(check.field === "ldc_notify_url" ? "付款通知 URL" : "付款返回 URL")}</strong> {t(messages[check.code] || messages.connection_failed)}</p>)}
  </div>;
}
