import { useQuery } from "@tanstack/react-query";
import { Check, Copy, Info } from "lucide-react";
import { useMemo, useState } from "react";
import { ControlButton, ControlInput, EmptyWorkState, WorkSurface } from "../../components/control-plane";
import { Tooltip, TooltipProvider } from "../../components/ui/Tooltip";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { readMigratedValue, removeStoredValues } from "../../lib/storage";
import { getEnvConfig, SYSTEM_ENV_CONFIG_QUERY_KEY, SYSTEM_ENV_STALE_MS } from "../systemConfig/api";

const PROXY_TOKEN_STORAGE_KEY = "prism.proxy-session-token";
const LEGACY_PROXY_TOKEN_KEYS = ["prismx.proxy-session-token"];
const TOKEN_PLACEHOLDER = "<token>";

type ProxyEndpoint = { scheme: string; host: string };

function loadStoredProxyToken(): string {
  if (typeof window === "undefined") {
    return "";
  }
  try {
    return readMigratedValue(window.sessionStorage, PROXY_TOKEN_STORAGE_KEY, LEGACY_PROXY_TOKEN_KEYS) ?? "";
  } catch {
    return "";
  }
}

function persistProxyToken(value: string): void {
  if (typeof window === "undefined") {
    return;
  }
  if (value) {
    try {
      window.sessionStorage.setItem(PROXY_TOKEN_STORAGE_KEY, value);
      removeStoredValues(window.sessionStorage, LEGACY_PROXY_TOKEN_KEYS);
    } catch {
      // The input remains available in component state for this session.
    }
  } else {
    try {
      removeStoredValues(window.sessionStorage, [PROXY_TOKEN_STORAGE_KEY, ...LEGACY_PROXY_TOKEN_KEYS]);
    } catch {
      // Nothing to clear.
    }
  }
}

function formatHostWithPort(hostname: string, port: number): string {
  const host =
    hostname.includes(":") && !hostname.startsWith("[")
      ? `[${hostname}]`
      : hostname;
  return port ? `${host}:${port}` : host;
}

function parseProxyEndpoint(value: string): ProxyEndpoint | null {
  try {
    const url = new URL(value);
    if (
      !["http:", "https:"].includes(url.protocol) ||
      url.username ||
      url.password ||
      url.pathname !== "/" ||
      url.search ||
      url.hash
    )
      return null;
    return { scheme: url.protocol.replace(/:$/, ""), host: url.host };
  } catch {
    return null;
  }
}

function currentProxyEndpoint(fallbackPort: number): ProxyEndpoint {
  const configured = import.meta.env.VITE_PROXY_BASE_URL?.trim();
  if (configured) {
    const endpoint = parseProxyEndpoint(configured);
    if (endpoint) return endpoint;
  }
  const hostname =
    typeof window === "undefined" ? "127.0.0.1" : window.location.hostname;
  return { scheme: "http", host: formatHostWithPort(hostname, fallbackPort) };
}

// Encode a URL segment/userinfo component, but keep the literal <token>
// placeholder readable so users can see where to paste the real token.
function encodeSegment(value: string): string {
  return value === TOKEN_PLACEHOLDER ? value : encodeURIComponent(value);
}

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

type ParsedTarget = { protocol: string; rest: string };

function parseTarget(raw: string): ParsedTarget | null {
  const trimmed = raw.trim();
  if (!trimmed) {
    return null;
  }
  const withScheme = /^[a-zA-Z][\w+.-]*:\/\//.test(trimmed)
    ? trimmed
    : `https://${trimmed}`;
  let url: URL;
  try {
    url = new URL(withScheme);
  } catch {
    return null;
  }
  const protocol = url.protocol.replace(/:$/, "").toLowerCase();
  if (protocol !== "http" && protocol !== "https") {
    return null;
  }
  const path = url.pathname === "/" ? "" : url.pathname;
  return { protocol, rest: `${url.host}${path}${url.search}` };
}

type CopyFieldProps = {
  label: string;
  value: string;
  hint?: string;
  copyLabel: string;
  copiedLabel: string;
};

/**
 * A credential or address that gets copied rather than typed.
 *
 * The value is mono because it is read character by character, and it wraps
 * instead of scrolling so no part of an address is ever hidden from the operator.
 */
function CopyField({
  label,
  value,
  hint,
  copyLabel,
  copiedLabel,
}: CopyFieldProps) {
  const [copied, setCopied] = useState(false);

  const handleCopy = async () => {
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(value);
      } else {
        const area = document.createElement("textarea");
        area.value = value;
        area.style.position = "fixed";
        area.style.opacity = "0";
        document.body.appendChild(area);
        area.select();
        document.execCommand("copy");
        document.body.removeChild(area);
      }
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  };

  return (
    <div className="cp-copy-field">
      <div className="cp-copy-field__head">
        <span className="cp-field__label">{label}</span>
        {hint ? <span className="cp-field__hint">{hint}</span> : null}
      </div>
      <div className="cp-copy-field__body">
        <code className="cp-code-block cp-copy-field__value" title={value}>{value || "—"}</code>
        <ControlButton
          variant="secondary"
          size="sm"
          onPress={() => void handleCopy()}
          aria-label={`${copied ? copiedLabel : copyLabel}：${label}`}
        >
          {copied ? <Check size={14} aria-hidden /> : <Copy size={14} aria-hidden />}
          {copied ? copiedLabel : copyLabel}
        </ControlButton>
      </div>
    </div>
  );
}

type PlatformAccessPanelProps = {
  platformName: string;
};

export function PlatformAccessPanel({
  platformName,
}: PlatformAccessPanelProps) {
  const { t } = useI18n();
  const [account, setAccount] = useState("");
  const [token, setToken] = useState(loadStoredProxyToken);
  const [target, setTarget] = useState("https://api.ipify.org");
  const [endpointOverride, setEndpointOverride] = useState("");

  // The shared env entry: this panel reads two fields off it under the same key
  // every other reader uses, so the url is fetched once and a save elsewhere can
  // refresh it instead of leaving this copy behind.
  const envQuery = useQuery({
    queryKey: SYSTEM_ENV_CONFIG_QUERY_KEY,
    queryFn: getEnvConfig,
    staleTime: SYSTEM_ENV_STALE_MS,
  });

  const env = envQuery.data ?? null;

  const handleTokenChange = (value: string) => {
    setToken(value);
    persistProxyToken(value.trim());
  };

  const urls = useMemo(() => {
    if (!env) {
      // No real config, no address. A made-up `:1080` and a made-up "token required"
      // are both addresses an operator would copy and the proxy would refuse; the
      // loading and failed states below cover this window instead.
      return { httpForward: "", socksForward: "", reverseUrl: "", curlForward: "", curlReverse: "" };
    }
    const proxyTokenSet = env.proxy_token_set;
    const endpoint = parseProxyEndpoint(endpointOverride) || currentProxyEndpoint(env.prism_port);
    const host = endpoint.host;
    const scheme = endpoint.scheme;
    const platform = platformName.trim() || "Default";
    const acc = account.trim();
    const tokenRaw = proxyTokenSet ? token.trim() : "";
    const sep = ".";

    // Raw identity/credential are used verbatim for the curl -U value.
    const identityRaw = acc ? `${platform}${sep}${acc}` : platform;
    // Encoded identity is reused for URL userinfo and reverse path segment.
    const identityEnc = acc
      ? `${encodeSegment(platform)}${sep}${encodeSegment(acc)}`
      : encodeSegment(platform);
    // forward token: literal placeholder only when auth is enabled but unset.
    const forwardToken = tokenRaw || (proxyTokenSet ? TOKEN_PLACEHOLDER : "");

    const forwardCredential = forwardToken
      ? `${identityRaw}:${forwardToken}`
      : identityRaw;
    const userInfo = forwardToken
      ? `${identityEnc}:${encodeSegment(forwardToken)}`
      : identityEnc;

    const httpForward = `${scheme}://${userInfo}@${host}`;
    const socksForward = `socks5h://${userInfo}@${host}`;

    const reverseTokenSeg = proxyTokenSet
      ? encodeSegment(tokenRaw || TOKEN_PLACEHOLDER)
      : "";
    const parsed = parseTarget(target);
    const reverseUrl = parsed
      ? `${scheme}://${host}/${reverseTokenSeg}/${identityEnc}/${parsed.protocol}/${parsed.rest}`
      : "";

    const curlForward = [
      "curl",
      "-x",
      shellQuote(`${scheme}://${host}`),
      "-U",
      shellQuote(forwardCredential),
      shellQuote("https://api.ipify.org"),
    ].join(" ");
    const curlReverse = reverseUrl ? `curl ${shellQuote(reverseUrl)}` : "";

    return { httpForward, socksForward, reverseUrl, curlForward, curlReverse };
  }, [env, endpointOverride, platformName, account, token, target]);

  if (envQuery.isError) {
    return <EmptyWorkState title={t("配置读取失败")} detail={formatApiErrorMessage(envQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void envQuery.refetch()}>{t("重试")}</ControlButton>} />;
  }
  if (!env) {
    return <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载配置")}</div>;
  }

  const proxyTokenSet = env.proxy_token_set;
  const inferredEndpoint = currentProxyEndpoint(env.prism_port);
  const copyLabel = t("复制");
  const copiedLabel = t("已复制");
  const tokenMissing = proxyTokenSet && !token.trim();
  const tokenInputValue = proxyTokenSet ? token : "";
  const endpointInvalid = Boolean(endpointOverride && !parseProxyEndpoint(endpointOverride));
  const tokenHint = t(
    "即后端 PRISM_PROXY_TOKEN。仅保存在浏览器本地，不会上传服务器。",
  );

  return (
    <TooltipProvider>
      <div className="cp-stack">
        <WorkSurface eyebrow="Delivery / access" title={t("接入方式")} description={t("填写账号与代理 token，一键复制正向/反向代理地址。")}>
          <div className="cp-form-grid">
            <ControlInput
              label={t("代理服务地址")}
              hint={endpointInvalid ? t("请输入不含凭证和路径的 HTTP(S) 地址") : undefined}
              className="font-mono"
              aria-invalid={endpointInvalid || undefined}
              placeholder={`${inferredEndpoint.scheme}://${inferredEndpoint.host}`}
              value={endpointOverride}
              onChange={(event) => setEndpointOverride(event.target.value)}
            />
            <ControlInput
              label={t("业务账号（可选）")}
              className="font-mono"
              placeholder={t("例如 user_tom，留空则只按平台路由")}
              value={account}
              onChange={(event) => setAccount(event.target.value)}
            />
          </div>
          <div className="cp-token-field">
            <div className="cp-token-field__label">
              <span className="cp-field__label">{t("代理 token")}</span>
              <Tooltip content={tokenHint}>
                <ControlButton variant="ghost" size="sm" isIconOnly aria-label={tokenHint}>
                  <Info size={13} aria-hidden />
                </ControlButton>
              </Tooltip>
            </div>
            <ControlInput
              aria-label={t("代理 token")}
              className="font-mono"
              type="password"
              placeholder={proxyTokenSet ? t("填写 PRISM_PROXY_TOKEN") : t("当前代理免认证，无需填写")}
              value={tokenInputValue}
              onChange={(event) => { if (proxyTokenSet) handleTokenChange(event.target.value); }}
              disabled={!proxyTokenSet}
              autoComplete="off"
            />
            {tokenMissing ? <p className="cp-muted">{t("尚未填写 token，地址中将以 <token> 占位，请替换为实际值。")}</p> : null}
          </div>
        </WorkSurface>

        <WorkSurface eyebrow="Delivery / forward proxy" title={t("正向代理")}>
          <div className="cp-form-stack">
            <CopyField label={t("HTTP 正向代理")} value={urls.httpForward} copyLabel={copyLabel} copiedLabel={copiedLabel} />
            <CopyField label={t("SOCKS5 正向代理")} value={urls.socksForward} copyLabel={copyLabel} copiedLabel={copiedLabel} />
            <CopyField label={t("curl 示例")} value={urls.curlForward} copyLabel={copyLabel} copiedLabel={copiedLabel} />
          </div>
        </WorkSurface>

        <WorkSurface eyebrow="Delivery / reverse proxy" title={t("反向代理")}>
          <div className="cp-form-stack">
            <ControlInput label={t("目标网址")} className="font-mono" placeholder={t("例如 https://api.ipify.org")} value={target} onChange={(event) => setTarget(event.target.value)} />
            {urls.reverseUrl ? (
              <>
                <CopyField label={t("反向代理地址")} value={urls.reverseUrl} copyLabel={copyLabel} copiedLabel={copiedLabel} />
                <CopyField label={t("curl 示例")} value={urls.curlReverse} copyLabel={copyLabel} copiedLabel={copiedLabel} />
              </>
            ) : <p className="cp-muted">{t("请输入合法的 http/https 目标网址以生成反向代理地址。")}</p>}
          </div>
        </WorkSurface>
      </div>
    </TooltipProvider>
  );
}