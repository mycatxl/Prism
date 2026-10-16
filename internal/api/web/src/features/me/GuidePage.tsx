import { useQuery } from "@tanstack/react-query";
import { Check, Copy, Network, RefreshCw, Server, ShieldCheck } from "lucide-react";
import { useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { PageHeader } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Table, TableWrap, TBody, TD, TH, THead, TR } from "../../components/ui/Table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../../components/ui/Tabs";
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

  const overviewQuery = useQuery({
    queryKey: ["me-overview"],
    queryFn: getOverview,
  });

  const copyText = (key: string, text: string, successMsg: string) => {
    // `copyToClipboard` resolves false instead of throwing when the page is not a
    // secure context, which is the only way this notice can appear there.
    void copyToClipboard(text).then((copied) => {
      if (!copied) {
        showToast("error", t("复制失败，请手动选择复制"));
        return;
      }
      setCopiedKey(key);
      showToast("success", successMsg);
      setTimeout(() => {
        setCopiedKey((prev) => (prev === key ? null : prev));
      }, 2000);
    });
  };

  const platforms = overviewQuery.data?.platforms ?? [];

  const forwardCurl = `curl -x http://<PRISM_HOST>:<PORT> \\
  -U "platform_id.account_id:pk_xxxx_yyyy" \\
  https://httpbin.org/ip`;

  const forwardPython = `import requests

proxies = {
    "http": "http://platform_id.account_id:pk_xxxx_yyyy@<PRISM_HOST>:<PORT>",
    "https": "http://platform_id.account_id:pk_xxxx_yyyy@<PRISM_HOST>:<PORT>",
}

resp = requests.get("https://httpbin.org/ip", proxies=proxies, timeout=10)
print(resp.json())`;

  const socks5Curl = `curl --socks5 <PRISM_HOST>:<PORT> \\
  --proxy-user "platform_id.account_id:pk_xxxx_yyyy" \\
  https://httpbin.org/ip`;

  const reverseCurl = `curl http://<PRISM_HOST>:<PORT>/pk_xxxx_yyyy/platform_id:account_id/https/httpbin.org/ip`;

  return (
    <div className="flex flex-col gap-6">
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <PageHeader
        title={t("接入指南")}
        description={t("查看客户端代理协议接入规范、认证格式以及当前租户可用平台列表。")}
        actions={
          <Button
            variant="secondary"
            size="sm"
            onClick={() => overviewQuery.refetch()}
            disabled={overviewQuery.isFetching}
          >
            <RefreshCw
              aria-hidden
              className={`size-4 text-ink-muted ${overviewQuery.isFetching ? "animate-spin" : ""}`}
            />
            <span>{t("刷新")}</span>
          </Button>
        }
      />

      {/* 核心要素 */}
      <Panel>
        <PanelHeader
          title={
            <div className="flex items-center gap-2">
              <Server aria-hidden className="size-4 text-ink-muted" />
              <span>{t("接入核心要素")}</span>
            </div>
          }
          description={t("客户端请求由 Prism 数据平面代理，必须携带合法的密钥与账号标识。")}
        />
        <PanelBody className="flex flex-col gap-4">
          <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
            <div className="flex flex-col gap-1.5 p-3 rounded-control border border-glass-edge bg-glass-soft">
              <div className="text-xs font-semibold text-ink-soft">{t("网关地址 (Host / Port)")}</div>
              <div className="text-sm font-mono text-ink select-all break-all">&lt;PRISM_HOST&gt;:&lt;PORT&gt;</div>
              <div className="text-xs text-ink-muted leading-relaxed">
                {t("具体网关 IP 与监听端口由平台运营方分配提供，租户侧无权读取底层接入点配置。")}
              </div>
            </div>

            <div className="flex flex-col gap-1.5 p-3 rounded-control border border-glass-edge bg-glass-soft">
              <div className="text-xs font-semibold text-ink-soft">{t("访问凭据 (Access Key)")}</div>
              <div className="text-sm font-mono text-ink select-all break-all">pk_&lt;key_id&gt;_&lt;secret&gt;</div>
              <div className="text-xs text-ink-muted leading-relaxed">
                {t("在「我的密钥」页面中生成。凭据明文仅在创建时展示一次，请妥善保管。")}
              </div>
            </div>

            <div className="flex flex-col gap-1.5 p-3 rounded-control border border-glass-edge bg-glass-soft">
              <div className="text-xs font-semibold text-ink-soft">{t("账号标识 (Identity)")}</div>
              <div className="text-sm font-mono text-ink select-all break-all">&lt;platform_id&gt;.&lt;account_id&gt;</div>
              <div className="text-xs text-ink-muted leading-relaxed">
                {t("用于区分目标平台与会话粘性账号；若无需指定平台或账号，可用通用占位符下划线。")}
              </div>
            </div>
          </div>
        </PanelBody>
      </Panel>

      {/* 可用平台列表 */}
      <Panel>
        <PanelHeader
          title={
            <div className="flex items-center gap-2">
              <Network aria-hidden className="size-4 text-ink-muted" />
              <span>{t("可用平台标识")}</span>
            </div>
          }
          description={t("当前租户绑定的可用平台代号，用于拼装认证标识中的 platform_id。")}
          meta={<Badge tone="neutral">{t("{{count}} 个平台", { count: platforms.length })}</Badge>}
        />
        <PanelBody>
          {overviewQuery.isLoading ? (
            <LoadingState label={t("正在加载可用平台...")} />
          ) : overviewQuery.isError ? (
            <ErrorState
              message={formatApiErrorMessage(overviewQuery.error, t)}
              onRetry={() => overviewQuery.refetch()}
            />
          ) : platforms.length === 0 ? (
            <EmptyState
              title={t("暂无可指定平台")}
              hint={t("当前套餐未绑定特定受限平台，认证时 platform_id 可使用下划线 _ 代替。")}
            />
          ) : (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>{t("平台代号 (platform_id)")}</TH>
                    <TH>{t("平台名称")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {platforms.map((plat) => (
                    <TR key={plat.platform_id}>
                      <TD>
                        <span className="font-mono text-xs font-semibold text-ink select-all">
                          {plat.platform_id}
                        </span>
                      </TD>
                      <TD>
                        <span className="text-sm text-ink-soft">{plat.platform_name}</span>
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
          )}
        </PanelBody>
      </Panel>

      {/* 协议接入指南与示例 */}
      <Panel>
        <PanelHeader
          title={
            <div className="flex items-center gap-2">
              <ShieldCheck aria-hidden className="size-4 text-ink-muted" />
              <span>{t("代理协议接入规范与代码示例")}</span>
            </div>
          }
          description={t("支持 HTTP 正向代理、SOCKS5 协议以及 HTTP 反向代理三种接入通道。")}
        />
        <PanelBody>
          <Tabs defaultValue="forward" className="flex flex-col gap-4">
            <TabsList>
              <TabsTrigger value="forward">{t("HTTP / HTTPS 正向代理")}</TabsTrigger>
              <TabsTrigger value="socks5">{t("SOCKS5 代理")}</TabsTrigger>
              <TabsTrigger value="reverse">{t("HTTP 反向代理")}</TabsTrigger>
            </TabsList>

            {/* Forward Proxy */}
            <TabsContent value="forward" className="flex flex-col gap-4">
              <div className="text-xs text-ink-soft leading-relaxed">
                {t(
                  "标准 HTTP / HTTPS 代理模式，支持 HTTP CONNECT 隧道。客户端使用 Proxy-Authorization Basic 头进行认证，用户名传入 <platform_id>.<account_id>，密码传入 Access Key 凭据。",
                )}
              </div>

              <div className="flex flex-col gap-2">
                <div className="flex items-center justify-between">
                  <span className="text-xs font-semibold text-ink-soft">{t("cURL 命令行示例")}</span>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => copyText("forwardCurl", forwardCurl, t("cURL 命令已复制"))}
                  >
                    {copiedKey === "forwardCurl" ? (
                      <Check aria-hidden className="size-3.5 text-live" />
                    ) : (
                      <Copy aria-hidden className="size-3.5 text-ink-muted" />
                    )}
                    <span>{t("复制")}</span>
                  </Button>
                </div>
                <pre className="p-3 rounded-control border border-glass-edge bg-glass-soft font-mono text-xs text-ink overflow-x-auto whitespace-pre">
                  <code>{forwardCurl}</code>
                </pre>
              </div>

              <div className="flex flex-col gap-2">
                <div className="flex items-center justify-between">
                  <span className="text-xs font-semibold text-ink-soft">{t("Python Requests 示例")}</span>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => copyText("forwardPython", forwardPython, t("Python 代码已复制"))}
                  >
                    {copiedKey === "forwardPython" ? (
                      <Check aria-hidden className="size-3.5 text-live" />
                    ) : (
                      <Copy aria-hidden className="size-3.5 text-ink-muted" />
                    )}
                    <span>{t("复制")}</span>
                  </Button>
                </div>
                <pre className="p-3 rounded-control border border-glass-edge bg-glass-soft font-mono text-xs text-ink overflow-x-auto whitespace-pre">
                  <code>{forwardPython}</code>
                </pre>
              </div>
            </TabsContent>

            {/* SOCKS5 */}
            <TabsContent value="socks5" className="flex flex-col gap-4">
              <div className="text-xs text-ink-soft leading-relaxed">
                {t(
                  "标准 SOCKS5 代理模式，遵循 RFC 1929 用户名/密码认证规范。用户名传入 <platform_id>.<account_id>，密码传入 Access Key 凭据。",
                )}
              </div>

              <div className="flex flex-col gap-2">
                <div className="flex items-center justify-between">
                  <span className="text-xs font-semibold text-ink-soft">{t("cURL SOCKS5 命令行示例")}</span>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => copyText("socks5Curl", socks5Curl, t("cURL 命令已复制"))}
                  >
                    {copiedKey === "socks5Curl" ? (
                      <Check aria-hidden className="size-3.5 text-live" />
                    ) : (
                      <Copy aria-hidden className="size-3.5 text-ink-muted" />
                    )}
                    <span>{t("复制")}</span>
                  </Button>
                </div>
                <pre className="p-3 rounded-control border border-glass-edge bg-glass-soft font-mono text-xs text-ink overflow-x-auto whitespace-pre">
                  <code>{socks5Curl}</code>
                </pre>
              </div>
            </TabsContent>

            {/* Reverse Proxy */}
            <TabsContent value="reverse" className="flex flex-col gap-4">
              <div className="text-xs text-ink-soft leading-relaxed">
                {t(
                  "HTTP 反向代理模式，适用于无法修改客户端代理设置的场景。客户端直接请求网关地址，并在 URL 路径首部携带认证凭据与目标地址。",
                )}
              </div>

              <div className="flex flex-col gap-1.5 p-3 rounded-control border border-glass-edge bg-glass-soft">
                <div className="text-xs font-semibold text-ink-soft">{t("URL 路径规范")}</div>
                <div className="font-mono text-xs text-ink select-all break-all">
                  /&lt;credential&gt;/&lt;platform_id&gt;:&lt;account_id&gt;/&lt;target_scheme&gt;/&lt;target_host&gt;/&lt;target_path&gt;
                </div>
                <div className="text-xs text-ink-muted">
                  {t("例如目标为 https://httpbin.org/ip 时，target_scheme 填 https，target_host 填 httpbin.org。")}
                </div>
              </div>

              <div className="flex flex-col gap-2">
                <div className="flex items-center justify-between">
                  <span className="text-xs font-semibold text-ink-soft">{t("cURL 反向代理命令行示例")}</span>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => copyText("reverseCurl", reverseCurl, t("cURL 命令已复制"))}
                  >
                    {copiedKey === "reverseCurl" ? (
                      <Check aria-hidden className="size-3.5 text-live" />
                    ) : (
                      <Copy aria-hidden className="size-3.5 text-ink-muted" />
                    )}
                    <span>{t("复制")}</span>
                  </Button>
                </div>
                <pre className="p-3 rounded-control border border-glass-edge bg-glass-soft font-mono text-xs text-ink overflow-x-auto whitespace-pre">
                  <code>{reverseCurl}</code>
                </pre>
              </div>
            </TabsContent>
          </Tabs>
        </PanelBody>
      </Panel>

      {/* 注意事项 */}
      <Panel>
        <PanelHeader
          title={t("接入注意事项")}
          description={t("请留意以下访问约束与配额规则，避免请求被网关拒绝。")}
        />
        <PanelBody className="flex flex-col gap-3 text-xs text-ink-soft leading-relaxed">
          <div className="flex items-start gap-2">
            <span className="font-semibold text-ink">•</span>
            <span>
              {t("密钥凭据一旦泄露请立即在「我的密钥」中删除。已删除或已过期的密钥将被网关立即拒绝。")}
            </span>
          </div>
          <div className="flex items-start gap-2">
            <span className="font-semibold text-ink">•</span>
            <span>
              {t("若密钥配置了 IP 白名单，非白名单来源的客户端连接将无法通过鉴权。")}
            </span>
          </div>
          <div className="flex items-start gap-2">
            <span className="font-semibold text-ink">•</span>
            <span>
              {t("当前并发数若超过套餐允许的最高并发数（max_concurrent），新发起的连接将被限流或拒绝。")}
            </span>
          </div>
          <div className="flex items-start gap-2">
            <span className="font-semibold text-ink">•</span>
            <span>
              {t("套餐配额用尽后将无法继续通过代理传输数据，请关注概览页面的用量统计与剩余流量。")}
            </span>
          </div>
        </PanelBody>
      </Panel>
    </div>
  );
}