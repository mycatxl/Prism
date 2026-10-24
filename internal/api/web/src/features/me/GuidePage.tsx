import { useQuery } from "@tanstack/react-query";
import { Check, Copy, Network, RefreshCw, ShieldCheck } from "lucide-react";
import { useState } from "react";
import {
  ControlButton,
  ControlTabs,
  EmptyWorkState,
  ObjectList,
  ObjectRow,
  PageFrame,
  QueryBadge,
  StatusMark,
  WorkSurface,
} from "../../components/control-plane";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { copyToClipboard } from "../../lib/clipboard";
import { formatApiErrorMessage } from "../../lib/error-message";
import { getOverview } from "./api";

export function GuidePage() {
  const { t } = useI18n();
  const { toasts, showToast, dismissToast } = useToast();
  const [copiedKey, setCopiedKey] = useState<string | null>(null);
  const [protocol, setProtocol] = useState("forward");
  const overviewQuery = useQuery({ queryKey: ["me-overview"], queryFn: getOverview });
  const platforms = overviewQuery.data?.platforms ?? [];
  const firstPlatform = platforms[0]?.platform_name ?? "platform_name";
  const gatewayAddr = typeof window === "undefined" ? "<PRISM_HOST>:<PORT>" : window.location.host;
  const gatewayOrigin = typeof window === "undefined" ? "http://<PRISM_HOST>:<PORT>" : window.location.origin;
  const forwardCurl = `curl -x ${gatewayOrigin} \\
  -U "${firstPlatform}.<account_id>:pk_xxxx_yyyy" \\
  https://httpbin.org/ip`;
  const forwardPython = `import requests

proxies = {
    "http": "http://${firstPlatform}.<account_id>:pk_xxxx_yyyy@${gatewayAddr}",
    "https": "http://${firstPlatform}.<account_id>:pk_xxxx_yyyy@${gatewayAddr}",
}

resp = requests.get("https://httpbin.org/ip", proxies=proxies, timeout=10)
print(resp.json())`;
  const socks5Curl = `curl --socks5 ${gatewayAddr} \\
  --proxy-user "${firstPlatform}.<account_id>:pk_xxxx_yyyy" \\
  https://httpbin.org/ip`;
  const reverseCurl = `curl ${gatewayOrigin}/pk_xxxx_yyyy/${firstPlatform}:<account_id>/https/httpbin.org/ip`;

  const copyText = (key: string, text: string, successMessage: string) => {
    void copyToClipboard(text).then((copied) => {
      if (!copied) {
        showToast("error", t("复制失败，请手动选择复制"));
        return;
      }
      setCopiedKey(key);
      showToast("success", successMessage);
      window.setTimeout(() => setCopiedKey((current) => current === key ? null : current), 2000);
    });
  };

  return (
    <PageFrame
      eyebrow="Self Service / integration"
      title={t("接入指南")}
      description={t("查看客户端代理协议接入规范、认证格式以及当前租户可用平台列表。")}
      meta={<StatusMark tone={overviewQuery.isError ? "warn" : overviewQuery.isSuccess ? "live" : "neutral"} label={overviewQuery.isError ? t("读取失败") : overviewQuery.isSuccess ? t("数据已同步") : t("等待数据")} />}
      actions={<ControlButton variant="secondary" size="sm" onPress={() => void overviewQuery.refetch()} loading={overviewQuery.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton>}
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      <WorkSurface eyebrow="Connection contract" title={t("接入核心要素")} description={t("客户端请求由 Prism 数据平面代理，必须携带合法的密钥与账号标识。")}>
        <div className="cp-data-grid">
          <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("网关地址 (Host / Port)")}</span><strong className="cp-data-grid__value cp-data-grid__value--text break-all">{gatewayAddr}</strong><span className="cp-data-grid__hint">{t("控制台与代理网关是同一入口时，这里就是本页地址；若运营方另设代理端口，以运营方提供的地址为准。")}</span></div>
          <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("访问凭据 (Access Key)")}</span><strong className="cp-data-grid__value cp-data-grid__value--text">pk_&lt;key_id&gt;_&lt;secret&gt;</strong><span className="cp-data-grid__hint">{t("在「我的密钥」页面中生成。凭据明文仅在创建时展示一次，请妥善保管。")}</span></div>
          <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("账号标识 (Identity)")}</span><strong className="cp-data-grid__value cp-data-grid__value--text">&lt;platform_name&gt;.&lt;account_id&gt;</strong><span className="cp-data-grid__hint">{t("点号前是平台名称，点号后是会话粘性账号。只填平台名称、不写点号也可用；整段留空则走默认平台。")}</span></div>
        </div>
      </WorkSurface>

      <WorkSurface eyebrow="Authorized catalog" title={t("可用平台标识")} description={t("当前租户可用的平台名称，直接填进认证标识的点号之前。")} meta={<QueryBadge label={t("{{count}} 个平台", { count: platforms.length })} />}>
        {overviewQuery.isLoading ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载可用平台...")}</div> : null}
        {overviewQuery.isError ? <EmptyWorkState title={t("读取失败")} detail={formatApiErrorMessage(overviewQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void overviewQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
        {!overviewQuery.isLoading && !overviewQuery.isError && platforms.length === 0 ? <EmptyWorkState title={t("暂无可用平台")} detail={t("当前套餐没有绑定特定平台，认证时把点号前的部分留空，请求会走默认平台。")} /> : null}
        {platforms.length > 0 ? <ObjectList>{platforms.map((platform) => <ObjectRow key={platform.platform_id} leading={<Network size={14} aria-hidden />} title={platform.platform_name} subtitle={platform.platform_id} facts={<span>{t("填在认证标识的点号之前")}</span>} status={<StatusMark tone="signal" label={t("可用")} />} />)}</ObjectList> : null}
      </WorkSurface>

      <WorkSurface eyebrow="Protocol examples" title={t("代理协议接入规范与代码示例")} description={t("支持 HTTP 正向代理、SOCKS5 协议以及 HTTP 反向代理三种接入通道。")}>
        <div className="cp-stack">
          <ControlTabs items={[{ id: "forward", label: t("HTTP / HTTPS 正向代理") }, { id: "socks5", label: t("SOCKS5 代理") }, { id: "reverse", label: t("HTTP 反向代理") }]} selectedKey={protocol} onSelectionChange={setProtocol} label={t("代理协议") } />
          {protocol === "forward" ? <div className="cp-stack"><p className="cp-muted">{t("标准 HTTP / HTTPS 代理模式，支持 HTTP CONNECT 隧道。客户端使用 Proxy-Authorization Basic 头进行认证，用户名传入 <platform_name>.<account_id>，密码传入 Access Key 凭据。")}</p><CodeExample label={t("cURL 命令行示例")} code={forwardCurl} copied={copiedKey === "forwardCurl"} onCopy={() => copyText("forwardCurl", forwardCurl, t("cURL 命令已复制"))} t={t} /><CodeExample label={t("Python Requests 示例")} code={forwardPython} copied={copiedKey === "forwardPython"} onCopy={() => copyText("forwardPython", forwardPython, t("Python 代码已复制"))} t={t} /></div> : null}
          {protocol === "socks5" ? <div className="cp-stack"><p className="cp-muted">{t("标准 SOCKS5 代理模式，遵循 RFC 1929 用户名/密码认证规范。用户名传入 <platform_name>.<account_id>，密码传入 Access Key 凭据。")}</p><CodeExample label={t("cURL SOCKS5 命令行示例")} code={socks5Curl} copied={copiedKey === "socks5Curl"} onCopy={() => copyText("socks5Curl", socks5Curl, t("cURL 命令已复制"))} t={t} /></div> : null}
          {protocol === "reverse" ? <div className="cp-stack"><p className="cp-muted">{t("HTTP 反向代理模式，适用于无法修改客户端代理设置的场景。客户端直接请求网关地址，并在 URL 路径首部携带认证凭据与目标地址。")}</p><div className="border border-rule bg-paper-inset p-3"><strong className="block text-xs text-ink-soft">{t("URL 路径规范")}</strong><code className="mt-2 block break-all text-xs text-ink">/&lt;credential&gt;/&lt;platform_name&gt;:&lt;account_id&gt;/&lt;target_scheme&gt;/&lt;target_host&gt;/&lt;target_path&gt;</code><span className="mt-2 block text-xs text-ink-faint">{t("例如目标为 https://httpbin.org/ip 时，target_scheme 填 https，target_host 填 httpbin.org。")}</span></div><CodeExample label={t("cURL 反向代理命令行示例")} code={reverseCurl} copied={copiedKey === "reverseCurl"} onCopy={() => copyText("reverseCurl", reverseCurl, t("cURL 命令已复制"))} t={t} /></div> : null}
        </div>
      </WorkSurface>

      <WorkSurface eyebrow="Operational constraints" title={t("接入注意事项")} description={t("请留意以下访问约束与配额规则，避免请求被网关拒绝。")}>
        <ObjectList>
          <ObjectRow leading={<ShieldCheck size={14} aria-hidden />} title={t("密钥凭据一旦泄露请立即在「我的密钥」中删除。已删除或已过期的密钥将被网关立即拒绝。")} />
          <ObjectRow leading={<ShieldCheck size={14} aria-hidden />} title={t("若密钥配置了 IP 白名单，非白名单来源的客户端连接将无法通过鉴权。")} />
          <ObjectRow leading={<ShieldCheck size={14} aria-hidden />} title={t("当前并发数若超过套餐允许的最高并发数（max_concurrent），新发起的连接将被限流或拒绝。")} />
          <ObjectRow leading={<ShieldCheck size={14} aria-hidden />} title={t("套餐配额用尽后将无法继续通过代理传输数据，请关注概览页面的用量统计与剩余流量。")} />
        </ObjectList>
      </WorkSurface>
    </PageFrame>
  );
}

function CodeExample({ label, code, copied, onCopy, t }: { label: string; code: string; copied: boolean; onCopy: () => void; t: (text: string, options?: Record<string, unknown>) => string }) {
  return <div className="cp-stack"><div className="flex items-center justify-between gap-3"><strong className="text-xs text-ink-soft">{label}</strong><ControlButton variant="secondary" size="sm" onPress={onCopy}>{copied ? <Check size={14} aria-hidden /> : <Copy size={14} aria-hidden />}{copied ? t("已复制") : t("复制")}</ControlButton></div><pre className="overflow-x-auto whitespace-pre border border-rule bg-paper-inset p-3 font-mono text-xs text-ink"><code>{code}</code></pre></div>;
}
