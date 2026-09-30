import { useQuery } from "@tanstack/react-query";
import { Check, Copy, Info } from "lucide-react";
import { useMemo, useState } from "react";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input } from "../../components/ui/Input";
import { Panel, PanelBody, PanelHeader } from "../../components/ui/Panel";
import { Tooltip, TooltipProvider } from "../../components/ui/Tooltip";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { readMigratedValue, removeStoredValues } from "../../lib/storage";
import { getEnvConfig } from "../systemConfig/api";

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
    <div className="min-w-0">
      <div className="flex items-baseline justify-between gap-2 pb-1">
        <span className="label">{label}</span>
        {hint ? <span className="text-2xs text-ink-faint">{hint}</span> : null}
      </div>
      <div className="flex items-start gap-2">
        <code
          className="min-w-0 flex-1 rounded-control border border-rule bg-paper-sunk px-2 py-1.5 font-mono text-xs break-all text-ink"
          title={value}
        >
          {value}
        </code>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => void handleCopy()}
          aria-label={`${copied ? copiedLabel : copyLabel}：${label}`}
        >
          {copied ? <Check size={14} /> : <Copy size={14} />}
          {copied ? copiedLabel : copyLabel}
        </Button>
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

  const envQuery = useQuery({
    queryKey: ["system-env-config"],
    queryFn: getEnvConfig,
    staleTime: 60_000,
  });

  const env = envQuery.data;
  const proxyTokenSet = env?.proxy_token_set ?? true;
  const inferredEndpoint = currentProxyEndpoint(env?.prism_port ?? 1080);
  const endpoint = parseProxyEndpoint(endpointOverride) || inferredEndpoint;
  const host = endpoint.host;
  const scheme = endpoint.scheme;

  const handleTokenChange = (value: string) => {
    setToken(value);
    persistProxyToken(value.trim());
  };

  const urls = useMemo(() => {
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
  }, [platformName, account, token, proxyTokenSet, host, scheme, target]);

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
      <div className="flex flex-col gap-3">
        <Panel>
          <PanelHeader
            title={t("接入方式")}
            description={t("填写账号与代理 token，一键复制正向/反向代理地址。")}
          />
          <PanelBody className="grid gap-3 sm:grid-cols-2">
            <Fieldset
              label={t("代理服务地址")}
              htmlFor="access-endpoint"
              hint={endpointInvalid ? t("请输入不含凭证和路径的 HTTP(S) 地址") : undefined}
            >
              <Input
                id="access-endpoint"
                className={cn("font-mono", endpointInvalid && "border-alert")}
                aria-invalid={endpointInvalid || undefined}
                placeholder={`${inferredEndpoint.scheme}://${inferredEndpoint.host}`}
                value={endpointOverride}
                onChange={(event) => setEndpointOverride(event.target.value)}
              />
              {endpointInvalid ? (
                <p className="text-xs text-alert">
                  {t("请输入不含凭证和路径的 HTTP(S) 地址")}
                </p>
              ) : null}
            </Fieldset>

            <Fieldset label={t("业务账号（可选）")} htmlFor="access-account">
              <Input
                id="access-account"
                className="font-mono"
                placeholder={t("例如 user_tom，留空则只按平台路由")}
                value={account}
                onChange={(event) => setAccount(event.target.value)}
              />
            </Fieldset>

            <div className="space-y-1 sm:col-span-2">
              <div className="flex items-center gap-1.5">
                <label htmlFor="access-token" className="text-xs font-medium text-ink-soft">
                  {t("代理 token")}
                </label>
                <Tooltip content={tokenHint}>
                  <Button
                    type="button"
                    variant="quiet"
                    size="icon"
                    aria-label={tokenHint}
                    className="text-ink-faint"
                  >
                    <Info size={13} aria-hidden />
                  </Button>
                </Tooltip>
              </div>
              <Input
                id="access-token"
                className="font-mono"
                type="password"
                placeholder={
                  proxyTokenSet
                    ? t("填写 PRISM_PROXY_TOKEN")
                    : t("当前代理免认证，无需填写")
                }
                value={tokenInputValue}
                onChange={(event) => {
                  if (proxyTokenSet) {
                    handleTokenChange(event.target.value);
                  }
                }}
                disabled={!proxyTokenSet}
                autoComplete="off"
              />
              {tokenMissing ? (
                <p className="max-w-[68ch] text-xs text-ink-faint">
                  {t("尚未填写 token，地址中将以 <token> 占位，请替换为实际值。")}
                </p>
              ) : null}
            </div>
          </PanelBody>
        </Panel>

        <Panel>
          <PanelHeader title={t("正向代理")} />
          <PanelBody className="space-y-3">
            <CopyField
              label={t("HTTP 正向代理")}
              value={urls.httpForward}
              copyLabel={copyLabel}
              copiedLabel={copiedLabel}
            />
            <CopyField
              label={t("SOCKS5 正向代理")}
              value={urls.socksForward}
              copyLabel={copyLabel}
              copiedLabel={copiedLabel}
            />
            <CopyField
              label={t("curl 示例")}
              value={urls.curlForward}
              copyLabel={copyLabel}
              copiedLabel={copiedLabel}
            />
          </PanelBody>
        </Panel>

        <Panel>
          <PanelHeader title={t("反向代理")} />
          <PanelBody className="space-y-3">
            <Fieldset label={t("目标网址")} htmlFor="access-target">
              <Input
                id="access-target"
                className="font-mono"
                placeholder={t("例如 https://api.ipify.org")}
                value={target}
                onChange={(event) => setTarget(event.target.value)}
              />
            </Fieldset>
            {urls.reverseUrl ? (
              <>
                <CopyField
                  label={t("反向代理地址")}
                  value={urls.reverseUrl}
                  copyLabel={copyLabel}
                  copiedLabel={copiedLabel}
                />
                <CopyField
                  label={t("curl 示例")}
                  value={urls.curlReverse}
                  copyLabel={copyLabel}
                  copiedLabel={copiedLabel}
                />
              </>
            ) : (
              <p className="max-w-[68ch] text-xs text-ink-faint">
                {t("请输入合法的 http/https 目标网址以生成反向代理地址。")}
              </p>
            )}
          </PanelBody>
        </Panel>
      </div>
    </TooltipProvider>
  );
}