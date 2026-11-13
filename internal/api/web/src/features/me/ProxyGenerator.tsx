import { Check, Copy, Eye, EyeOff, KeyRound, Route } from "lucide-react";
import { useState } from "react";
import {
  ControlButton,
  ControlInput,
  ControlSelect,
  ControlTabs,
  QueryBadge,
  WorkspaceCard,
} from "../../components/control-plane";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { copyToClipboard } from "../../lib/clipboard";
import type { SelfPlatform } from "./types";

type Protocol = "http" | "socks5";
type SnippetFormat = "curl" | "python" | "playwright" | "clash" | "raw";

function splitGateway(value: string): { host: string; port: string } {
  const clean = value.replace(/^https?:\/\//, "").replace(/^socks5?:\/\//, "").replace(/\/$/, "");
  if (clean.startsWith("[")) {
    const bracket = clean.indexOf("]");
    return { host: clean.slice(0, bracket + 1), port: clean.slice(bracket + 2) || "1080" };
  }
  const separator = clean.lastIndexOf(":");
  if (separator < 0) return { host: clean, port: "1080" };
  return { host: clean.slice(0, separator), port: clean.slice(separator + 1) || "1080" };
}

function buildSnippet({
  format,
  protocol,
  gateway,
  username,
  credential,
  label,
}: {
  format: SnippetFormat;
  protocol: Protocol;
  gateway: string;
  username: string;
  credential: string;
  label: string;
}) {
  const { host, port } = splitGateway(gateway);
  const scheme = protocol === "socks5" ? "socks5" : "http";
  const proxyUrl = `${scheme}://${username}:${credential}@${host}:${port}`;
  if (format === "raw") return `${host}:${port}:${username}:${credential}`;
  if (format === "curl") {
    return protocol === "socks5"
      ? `curl --socks5-hostname ${host}:${port} \\\n  --proxy-user "${username}:${credential}" \\\n  https://api.ipify.org`
      : `curl --proxy http://${host}:${port} \\\n  --proxy-user "${username}:${credential}" \\\n  https://api.ipify.org`;
  }
  if (format === "python") {
    return `import requests

proxy = "${proxyUrl}"
response = requests.get(
    "https://api.ipify.org?format=json",
    proxies={"http": proxy, "https": proxy},
    timeout=20,
)
print(response.json())`;
  }
  if (format === "playwright") {
    return `import { chromium } from "playwright";

const browser = await chromium.launch({
  proxy: {
    server: "${scheme}://${host}:${port}",
    username: "${username}",
    password: "${credential}",
  },
});`;
  }
  return `proxies:
  - name: "${label}"
    type: ${protocol}
    server: ${host}
    port: ${port}
    username: "${username}"
    password: "${credential}"`;
}

export function ProxyGenerator({ platforms }: { platforms: SelfPlatform[] }) {
  const { t } = useI18n();
  const { showToast } = useToast();
  const defaultGateway = typeof window === "undefined" ? "proxy.example.com:1080" : window.location.host;
  const [gateway, setGateway] = useState(defaultGateway);
  const [platformId, setPlatformId] = useState(platforms[0]?.platform_id ?? "");
  const [protocol, setProtocol] = useState<Protocol>("http");
  const [format, setFormat] = useState<SnippetFormat>("curl");
  const [account, setAccount] = useState("session-01");
  const [credential, setCredential] = useState("");
  const [revealCredential, setRevealCredential] = useState(false);
  const [copied, setCopied] = useState(false);

  const selected = platforms.find((platform) => platform.platform_id === platformId);
  const platformName = selected?.platform_name || "default";
  const username = `${platformName}${account.trim() ? `.${account.trim()}` : ""}`;
  const effectiveCredential = credential.trim() || "pk_your_key";
  const snippet = buildSnippet({
    format,
    protocol,
    gateway: gateway.trim() || defaultGateway,
    username,
    credential: effectiveCredential,
    label: `Prism ${platformName}`,
  });

  const platformItems = [
    { id: "", label: <span className="proxy-generator__option">{t("默认路由")}</span> },
    ...platforms.map((platform) => ({
      id: platform.platform_id,
      label: <span className="proxy-generator__option">{platform.platform_name}</span>,
    })),
  ];

  const copy = () => {
    void copyToClipboard(snippet).then((ok) => {
      if (!ok) {
        showToast("error", t("复制失败，请手动选择复制"));
        return;
      }
      setCopied(true);
      showToast("success", t("代理配置已复制"));
      window.setTimeout(() => setCopied(false), 1_800);
    });
  };

  return (
    <WorkspaceCard
      className="proxy-generator"
      title={t("极速提取台")}
      description={t("选择路由与协议，生成可直接使用的代理配置。")}
      showDescription
      meta={<QueryBadge tone="signal" label={t("实时生成")} />}
      actions={<Route size={18} aria-hidden />}
    >
      <div className="proxy-generator__layout">
        <div className="proxy-generator__controls">
          <ControlSelect
            label={t("平台名称")}
            items={platformItems}
            selectedKey={platformId}
            onSelectionChange={(key) => setPlatformId(key ?? "")}
          />
          <p className="workspace-muted">{selected?.measured_countries?.length
            ? t("已观测出口国家：{{countries}}；不保证每次连接的国家。", { countries: selected.measured_countries.join(", ") })
            : t("暂无已观测出口国家；平台名称不代表地理位置。")}</p>
          <ControlTabs
            label={t("代理协议")}
            selectedKey={protocol}
            onSelectionChange={(value) => setProtocol(value as Protocol)}
            items={[{ id: "http", label: "HTTP" }, { id: "socks5", label: "SOCKS5" }]}
          />
          <div className="proxy-generator__field-grid">
            <ControlInput label={t("网关地址")} value={gateway} onChange={(event) => setGateway(event.target.value)} placeholder="proxy.example.com:1080" />
            <ControlInput label={t("粘性会话")} value={account} onChange={(event) => setAccount(event.target.value)} placeholder="session-01" />
          </div>
          <div className="proxy-generator__credential">
            <ControlInput
              label={t("访问密钥")}
              type={revealCredential ? "text" : "password"}
              value={credential}
              onChange={(event) => setCredential(event.target.value)}
              placeholder="pk_..."
              autoComplete="off"
              spellCheck={false}
            />
            <ControlButton
              variant="ghost"
              size="icon"
              aria-label={revealCredential ? t("隐藏密钥") : t("显示密钥")}
              onPress={() => setRevealCredential((visible) => !visible)}
            >
              {revealCredential ? <EyeOff size={16} aria-hidden /> : <Eye size={16} aria-hidden />}
            </ControlButton>
          </div>
          <div className="proxy-generator__identity">
            <span>{platformName}</span>
            <KeyRound size={15} aria-hidden />
            <code>{username}</code>
          </div>
        </div>

        <div className="proxy-generator__output">
          <ControlTabs
            label={t("输出格式")}
            selectedKey={format}
            onSelectionChange={(value) => setFormat(value as SnippetFormat)}
            items={[
              { id: "curl", label: "curl" },
              { id: "python", label: "Python" },
              { id: "playwright", label: "Playwright" },
              { id: "clash", label: "Clash" },
              { id: "raw", label: t("原始格式") },
            ]}
          />
          <pre className="proxy-generator__code"><code>{snippet}</code></pre>
          <ControlButton variant="primary" size="sm" onPress={copy}>
            {copied ? <Check size={15} aria-hidden /> : <Copy size={15} aria-hidden />}
            {copied ? t("已复制") : t("复制配置")}
          </ControlButton>
        </div>
      </div>
    </WorkspaceCard>
  );
}
