import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ChevronRight, KeyRound, RefreshCw, Undo2 } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input } from "../../components/ui/Input";
import { Panel, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Readout, ReadoutCell, ReadoutStrip } from "../../components/ui/Readout";
import { Sheet } from "../../components/ui/Sheet";
import { Switch } from "../../components/ui/Switch";
import { TBody, TD, TDNum, TH, THead, TR, Table, TableWrap } from "../../components/ui/Table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../../components/ui/Tabs";
import { Textarea } from "../../components/ui/Textarea";
import { ToastContainer } from "../../components/ui/Toast";
import { cn } from "../../lib/cn";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatDateTime, formatGoDuration } from "../../lib/time";
import {
  listIntelChecks,
  listIntelProviders,
  patchIntelCheck,
  patchIntelProvider,
  resumeIntelProvider,
} from "./api";
import type { IntelConfigValue, IntelProvider, IntelProviderPatch } from "./types";

// WP09 §4/§5.4: the data source and unlock check settings surface. Keys are
// write-only here as well: the API reports has_key and the form never renders a
// stored credential (R6).

type ShowToast = (tone: "success" | "error", text: string) => void;

function formatNs(value: number): string {
  if (!Number.isFinite(value) || value <= 0) {
    return "-";
  }
  const date = new Date(Math.round(value / 1_000_000));
  if (Number.isNaN(date.getTime())) {
    return "-";
  }
  return formatDateTime(date.toISOString());
}

/**
 * Two i18n entries in this surface are authored as suffixes so they can be
 * appended to a sentence (" · calibrated {{date}}"). A table cell is not a
 * sentence, so the separator is dropped for display; the key itself is unchanged.
 */
function withoutSeparator(text: string): string {
  return text.replace(/^[\s·]+/, "");
}

// nowNs is the timestamp of the last providers response, taken from the query
// state, so rendering never samples the clock itself.
function ProviderState({ provider, nowNs }: { provider: IntelProvider; nowNs: number }) {
  const { t } = useI18n();
  const usage = provider.usage;
  const badges: ReactNode[] = [];
  if (usage.paused) {
    badges.push(
      <Badge key="paused" tone="alert" dot>
        {t("已暂停")} {usage.error_code ? `(${usage.error_code})` : ""}
      </Badge>,
    );
  }
  if (usage.blocked_until_ns > 0 && usage.blocked_until_ns > nowNs) {
    badges.push(
      <Badge key="blocked" tone="warn">
        {t("已限流至 {{time}}", { time: formatNs(usage.blocked_until_ns) })}
      </Badge>,
    );
  }
  if (usage.exhausted) {
    badges.push(
      <Badge key="exhausted" tone="warn">
        {t("今日额度已用尽")}
      </Badge>,
    );
  }
  if (provider.requires_key && !provider.has_key) {
    badges.push(
      <Badge key="key" tone="warn">
        {t("缺少密钥")}
      </Badge>,
    );
  }
  if (!provider.enabled) {
    badges.push(
      <Badge key="disabled" tone="neutral">
        {t("已停用")}
      </Badge>,
    );
  }
  if (badges.length === 0) {
    badges.push(
      <Badge key="ok" tone="signal" dot>
        {t("运行正常")}
      </Badge>,
    );
  }
  return <div className="flex flex-wrap items-center gap-1">{badges}</div>;
}

function ProviderRow({
  provider,
  nowNs,
  editing,
  onOpen,
  onSaved,
  showToast,
}: {
  provider: IntelProvider;
  nowNs: number;
  editing: boolean;
  onOpen: (id: string) => void;
  onSaved: () => Promise<void>;
  showToast: ShowToast;
}) {
  const { t } = useI18n();
  const enabledMutation = useMutation({
    mutationFn: (enabled: boolean) => patchIntelProvider(provider.id, { enabled }),
    onSuccess: async () => {
      await onSaved();
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const limitLabel = provider.daily_limit > 0 ? provider.daily_limit : t("不限");
  const enableLabel = t("启用数据源 {{id}}", { id: provider.id });

  return (
    <TR selected={editing}>
      <TD>
        <button
          type="button"
          onClick={() => onOpen(provider.id)}
          aria-label={provider.name}
          aria-haspopup="dialog"
          aria-expanded={editing}
          className="group flex max-w-[40ch] items-center gap-1 text-left"
        >
          <span className="truncate text-sm font-medium text-ink underline-offset-2 group-hover:underline">
            {provider.name}
          </span>
          <ChevronRight size={13} aria-hidden className="shrink-0 text-ink-faint" />
        </button>
        <div className="mt-0.5 flex flex-wrap items-center gap-1.5">
          <span className="readout text-2xs text-ink-faint">{provider.id}</span>
          {provider.category ? <Badge tone="outline">{provider.category}</Badge> : null}
          {provider.via_node ? <Badge tone="outline">{t("经节点查询")}</Badge> : null}
          {provider.direct ? <Badge tone="outline">{t("服务器直连查询")}</Badge> : null}
        </div>
        {provider.terms ? (
          <p className="mt-1 max-w-[40ch] truncate text-2xs text-ink-faint" title={provider.terms}>
            {provider.terms}
          </p>
        ) : null}
      </TD>
      <TD>
        <ProviderState provider={provider} nowNs={nowNs} />
      </TD>
      <TDNum>{t("今日用量 {{used}} / {{limit}}", { used: provider.usage.used, limit: limitLabel })}</TDNum>
      <TDNum>{provider.qps}</TDNum>
      <TDNum>{formatGoDuration(provider.ttl)}</TDNum>
      <TD>
        {provider.requires_key ? (
          provider.has_key ? (
            <Badge tone="signal">{t("已配置密钥")}</Badge>
          ) : (
            <Badge tone="warn">{t("缺少密钥")}</Badge>
          )
        ) : (
          <span className="text-xs text-ink-faint">—</span>
        )}
      </TD>
      <TD className="text-right">
        <Switch
          checked={provider.enabled}
          disabled={enabledMutation.isPending}
          aria-label={enableLabel}
          title={enableLabel}
          onCheckedChange={(checked) => enabledMutation.mutate(checked)}
        />
      </TD>
    </TR>
  );
}

/**
 * One data source, opened from its row. The drawer holds every write path the
 * card used to: quota, rate, TTL, config JSON and the write-only API key.
 */
function ProviderEditor({
  provider,
  nowNs,
  showToast,
  onSaved,
  onClose,
}: {
  provider: IntelProvider;
  nowNs: number;
  showToast: ShowToast;
  onSaved: () => Promise<void>;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [dailyLimit, setDailyLimit] = useState(String(provider.daily_limit));
  const [qps, setQps] = useState(String(provider.qps));
  const [ttl, setTtl] = useState(provider.ttl);
  const [apiKey, setApiKey] = useState("");
  const [configText, setConfigText] = useState(() => JSON.stringify(provider.config ?? {}, null, 2));

  useEffect(() => {
    setDailyLimit(String(provider.daily_limit));
    setQps(String(provider.qps));
    setTtl(provider.ttl);
    setApiKey("");
    setConfigText(JSON.stringify(provider.config ?? {}, null, 2));
  }, [provider]);

  const saveMutation = useMutation({
    mutationFn: (patch: IntelProviderPatch) => patchIntelProvider(provider.id, patch),
    onSuccess: async () => {
      await onSaved();
      showToast("success", t("数据源 {{id}} 已更新", { id: provider.id }));
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const resumeMutation = useMutation({
    mutationFn: () => resumeIntelProvider(provider.id),
    onSuccess: async () => {
      await onSaved();
      showToast("success", t("数据源 {{id}} 已解除暂停", { id: provider.id }));
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const parseConfig = (): Record<string, IntelConfigValue> => {
    const raw = configText.trim();
    const next: Record<string, IntelConfigValue> = {};
    if (raw) {
      let parsed: unknown;
      try {
        parsed = JSON.parse(raw);
      } catch {
        throw new Error(t("配置必须是合法的 JSON 对象"));
      }
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
        throw new Error(t("配置必须是合法的 JSON 对象"));
      }
      for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
        if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") {
          next[key] = value;
        } else if (Array.isArray(value) && value.every((entry) => typeof entry === "string")) {
          next[key] = value as string[];
        } else {
          throw new Error(t("配置 {{key}} 的值必须是字符串、数字、布尔值或字符串数组", { key }));
        }
      }
    }
    // A key removed from the editor is removed server-side by a null value.
    for (const key of Object.keys(provider.config ?? {})) {
      if (!(key in next)) {
        next[key] = null;
      }
    }
    return next;
  };

  const submitSettings = () => {
    try {
      const limit = Number(dailyLimit);
      if (!Number.isInteger(limit) || limit < 0) {
        throw new Error(t("每日额度必须是非负整数"));
      }
      const rate = Number(qps);
      if (!Number.isFinite(rate) || rate < 0) {
        throw new Error(t("QPS 必须是非负数"));
      }
      saveMutation.mutate({
        daily_limit: limit,
        qps: rate,
        ttl: ttl.trim(),
        config: parseConfig(),
      });
    } catch (error) {
      showToast("error", error instanceof Error ? error.message : String(error));
    }
  };

  const submitKey = (key: string) => {
    saveMutation.mutate({ api_key: key });
  };

  const pending = saveMutation.isPending || resumeMutation.isPending;
  const needsKey = provider.requires_key && !provider.has_key;
  const rateLimited = provider.usage.blocked_until_ns > 0 && provider.usage.blocked_until_ns > nowNs;

  return (
    <Sheet
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      title={provider.name}
      description={provider.id}
      width="lg"
      footer={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Button
            size="sm"
            variant="danger"
            disabled={pending || !provider.has_key}
            onClick={() => submitKey("")}
          >
            {t("清除密钥")}
          </Button>
          <div className="flex flex-wrap items-center gap-1.5">
            {provider.usage.paused || rateLimited ? (
              <Button size="sm" variant="secondary" disabled={pending} onClick={() => resumeMutation.mutate()}>
                <Undo2 size={14} aria-hidden /> {t("解除暂停")}
              </Button>
            ) : null}
            <Button size="sm" variant="primary" onClick={submitSettings} disabled={pending}>
              {saveMutation.isPending ? t("保存中...") : t("保存设置")}
            </Button>
          </div>
        </div>
      }
    >
      <div className="space-y-5">
        <ReadoutStrip className="grid-cols-3">
          <ReadoutCell className="px-3 py-2.5">
            <Readout size="sm" label={t("队列大小")} value={provider.usage.queued} />
          </ReadoutCell>
          <ReadoutCell className="px-3 py-2.5">
            <Readout size="sm" label={t("运行中")} value={provider.usage.running} />
          </ReadoutCell>
          <ReadoutCell className="px-3 py-2.5">
            <Readout
              size="sm"
              tone={provider.usage.failed > 0 ? "alert" : "ink"}
              label={t("失败")}
              value={provider.usage.failed}
            />
          </ReadoutCell>
        </ReadoutStrip>

        {needsKey ? (
          <p className="flex items-start gap-2 text-xs leading-relaxed text-warn">
            <AlertTriangle size={13} aria-hidden className="mt-0.5 shrink-0" />
            {t("该数据源需要密钥才能运行，配置后会自动启用。")}
          </p>
        ) : null}

        <div className="grid gap-4 sm:grid-cols-3">
          <Fieldset label={t("每日额度")} htmlFor={`intel-limit-${provider.id}`}>
            <Input
              id={`intel-limit-${provider.id}`}
              className="font-mono"
              value={dailyLimit}
              inputMode="numeric"
              onChange={(event) => setDailyLimit(event.target.value)}
            />
            <p className="text-xs text-ink-faint">
              {t("0 表示不限；默认 {{def}}", {
                def: provider.default_daily_limit > 0 ? provider.default_daily_limit : t("不限"),
              })}
              {provider.max_daily_limit ? t("，上限 {{max}}", { max: provider.max_daily_limit }) : ""}
            </p>
          </Fieldset>

          <Fieldset label={t("每秒请求数 (QPS)")} htmlFor={`intel-qps-${provider.id}`}>
            <Input
              id={`intel-qps-${provider.id}`}
              className="font-mono"
              value={qps}
              inputMode="decimal"
              onChange={(event) => setQps(event.target.value)}
            />
            <p className="text-xs text-ink-faint">{t("0 表示不限；默认 {{def}}", { def: provider.default_qps })}</p>
          </Fieldset>

          <Fieldset label={t("证据有效期 (TTL)")} htmlFor={`intel-ttl-${provider.id}`}>
            <Input
              id={`intel-ttl-${provider.id}`}
              className="font-mono"
              value={ttl}
              placeholder={provider.default_ttl}
              onChange={(event) => setTtl(event.target.value)}
            />
            <p className="text-xs text-ink-faint">
              {t("Go 时长格式，例如 24h；默认 {{def}}", { def: provider.default_ttl })}
            </p>
          </Fieldset>
        </div>

        {provider.credential_fields?.length ? (
          <p className="text-xs leading-relaxed text-ink-faint">
            {t("该数据源的凭据字段在“配置”中设置：{{fields}}", {
              fields: provider.credential_fields.join(", "),
            })}
          </p>
        ) : null}

        <Fieldset
          label={t("配置 (JSON)")}
          htmlFor={`intel-config-${provider.id}`}
          hint={t("例如 DNSBL 的 zones 与 resolver；密钥类字段只写入不显示。")}
        >
          <Textarea
            id={`intel-config-${provider.id}`}
            className="font-mono"
            rows={5}
            value={configText}
            onChange={(event) => setConfigText(event.target.value)}
          />
        </Fieldset>

        <div className="border-t border-rule pt-4">
          <Fieldset
            label={provider.has_key ? t("更新 API Key（留空则保持不变）") : t("API Key")}
            htmlFor={`intel-key-${provider.id}`}
            hint={t("密钥只写入，接口永不返回；保存后立即对运行中的调度生效。")}
          >
            <div className="flex items-start gap-1.5">
              <Input
                id={`intel-key-${provider.id}`}
                className="min-w-0 flex-1 font-mono"
                type="password"
                autoComplete="off"
                spellCheck={false}
                value={apiKey}
                placeholder={provider.has_key ? t("已配置") : t("未配置")}
                onChange={(event) => setApiKey(event.target.value)}
              />
              <Button
                size="sm"
                variant="secondary"
                className="shrink-0"
                disabled={pending || apiKey.trim() === ""}
                onClick={() => submitKey(apiKey.trim())}
              >
                <KeyRound size={14} aria-hidden /> {t("保存密钥")}
              </Button>
            </div>
          </Fieldset>
        </div>

        {provider.updated_at_ns > 0 ? (
          <p className="text-xs text-ink-faint">
            {t("最近修改 {{time}}", { time: formatNs(provider.updated_at_ns) })}
          </p>
        ) : null}
      </div>
    </Sheet>
  );
}

function CheckRow({
  id,
  name,
  version,
  category,
  source,
  path,
  calibrated,
  enabled,
  enabledOverride,
  ttl,
  timeout,
  pending,
  onToggle,
}: {
  id: string;
  name: string;
  version: number;
  category: string;
  source: string;
  path?: string;
  calibrated?: string;
  enabled: boolean;
  enabledOverride: boolean;
  ttl: string;
  timeout: string;
  pending: boolean;
  onToggle: (enabled: boolean) => void;
}) {
  const { t } = useI18n();
  const toggleLabel = t("启用检测 {{id}}", { id });

  return (
    <TR>
      <TD>
        <p className="text-sm font-medium text-ink">{name}</p>
        <div className="mt-0.5 flex flex-wrap items-center gap-1.5">
          <span className="readout text-2xs text-ink-faint">{id}</span>
          <span className="readout text-2xs text-ink-faint">v{version}</span>
          <Badge tone={source === "user" ? "signal" : "neutral"}>{source}</Badge>
          {category ? <Badge tone="outline">{category}</Badge> : null}
          {enabledOverride ? (
            <Badge tone="outline" title={t(" · 已由设置页覆盖")}>
              {withoutSeparator(t(" · 已由设置页覆盖"))}
            </Badge>
          ) : null}
        </div>
        <p className="mt-0.5 text-xs text-ink-faint">
          {path ? t("用户规则文件 {{path}}", { path }) : t("内置规则")}
          {calibrated ? ` ${withoutSeparator(t(" · 校准日期 {{date}}", { date: calibrated }))}` : ""}
        </p>
      </TD>
      <TDNum>{formatGoDuration(ttl)}</TDNum>
      <TDNum>{formatGoDuration(timeout)}</TDNum>
      <TD className="text-right">
        <Switch
          checked={enabled}
          disabled={pending}
          aria-label={toggleLabel}
          title={toggleLabel}
          onCheckedChange={onToggle}
        />
      </TD>
    </TR>
  );
}

/**
 * Data sources and unlock checks.
 *
 * A row per source, with the numbers that decide whether it can be used — quota
 * spent, rate, TTL and whether a key is present — on one baseline. Everything
 * that writes lives in the drawer, so the list stays scannable.
 */
export function IntelSettingsPage() {
  const { t } = useI18n();
  const { toasts, showToast, dismissToast } = useToast();
  const queryClient = useQueryClient();
  const [tab, setTab] = useState<"providers" | "checks">("providers");
  const [editingId, setEditingId] = useState<string | null>(null);

  const providersQuery = useQuery({
    queryKey: ["intel-providers"],
    queryFn: listIntelProviders,
    refetchInterval: 30_000,
  });
  const checksQuery = useQuery({
    queryKey: ["intel-checks"],
    queryFn: listIntelChecks,
    refetchInterval: 60_000,
  });

  const invalidateProviders = async () => {
    await queryClient.invalidateQueries({ queryKey: ["intel-providers"] });
  };
  const invalidateChecks = async () => {
    await queryClient.invalidateQueries({ queryKey: ["intel-checks"] });
  };

  const checkMutation = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) => patchIntelCheck(id, enabled),
    onSuccess: async () => {
      await invalidateChecks();
      showToast("success", t("检测开关已保存"));
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["intel-providers"] });
    void queryClient.invalidateQueries({ queryKey: ["intel-checks"] });
  };

  const providers = providersQuery.data?.items ?? [];
  const checks = checksQuery.data?.items ?? [];
  const loadErrors = checksQuery.data?.load_errors ?? [];
  const providersFetchedAtNs = providersQuery.dataUpdatedAt * 1_000_000;

  const activeQuery = tab === "providers" ? providersQuery : checksQuery;
  const editingProvider = providers.find((provider) => provider.id === editingId) ?? null;

  return (
    <section className="min-h-full">
      <header className="border-b border-rule bg-paper-raised px-4 py-3 lg:px-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h1 className="text-xl">{t("数据源与解锁检测")}</h1>
          <Button size="sm" variant="secondary" onClick={refresh} disabled={activeQuery.isFetching}>
            <RefreshCw size={14} aria-hidden className={cn(activeQuery.isFetching && "animate-spin")} />
            {t("刷新")}
          </Button>
        </div>
      </header>

      <ReadoutStrip className="grid-cols-2">
        <ReadoutCell>
          <Readout label={t("数据源")} value={providers.length} />
        </ReadoutCell>
        <ReadoutCell>
          <Readout label={t("解锁检测")} value={checks.length} />
        </ReadoutCell>
      </ReadoutStrip>

      <div className="px-4 py-4 lg:px-6">
        <Tabs
          value={tab}
          onValueChange={(value) => setTab(value === "checks" ? "checks" : "providers")}
        >
          <TabsList>
            <TabsTrigger value="providers">{t("数据源")}</TabsTrigger>
            <TabsTrigger value="checks">{t("解锁检测")}</TabsTrigger>
          </TabsList>

          <TabsContent value="providers" className="pt-4">
            {providersQuery.isError ? (
              <ErrorState
                message={formatApiErrorMessage(providersQuery.error, t)}
                onRetry={() => void providersQuery.refetch()}
              />
            ) : null}

            {providersQuery.isPending ? <LoadingState label={t("正在加载数据源...")} /> : null}

            {!providersQuery.isPending && !providersQuery.isError ? (
              providers.length === 0 ? (
                <EmptyState className="py-10" title={t("暂无数据源")} />
              ) : (
                <Panel>
                  <PanelHeader title={t("数据源")} />
                  <TableWrap>
                    <Table className="min-w-[52rem]">
                      <THead>
                        <TR>
                          <TH>{t("数据源")}</TH>
                          <TH>{t("状态")}</TH>
                          <TH className="text-right">{t("每日额度")}</TH>
                          <TH className="text-right">{t("每秒请求数 (QPS)")}</TH>
                          <TH className="text-right">{t("证据有效期 (TTL)")}</TH>
                          <TH>API Key</TH>
                          <TH className="text-right">{t("启用")}</TH>
                        </TR>
                      </THead>
                      <TBody>
                        {providers.map((provider) => (
                          <ProviderRow
                            key={provider.id}
                            provider={provider}
                            nowNs={providersFetchedAtNs}
                            editing={provider.id === editingId}
                            onOpen={setEditingId}
                            onSaved={invalidateProviders}
                            showToast={showToast}
                          />
                        ))}
                      </TBody>
                    </Table>
                  </TableWrap>
                </Panel>
              )
            ) : null}
          </TabsContent>

          <TabsContent value="checks" className="pt-4">
            {checksQuery.isError ? (
              <ErrorState
                message={formatApiErrorMessage(checksQuery.error, t)}
                onRetry={() => void checksQuery.refetch()}
              />
            ) : null}

            {checksQuery.isPending ? <LoadingState label={t("正在加载检测规则...")} /> : null}

            {!checksQuery.isPending && !checksQuery.isError ? (
              <Panel>
                <PanelHeader
                  title={t("解锁检测")}
                  description={t("用户规则放在 $PRISM_STATE_DIR/checks.d/*.yaml，每 60 秒热加载。")}
                />

                {loadErrors.length > 0 ? (
                  <div className="border-b border-rule bg-warn-wash px-4 py-3">
                    <p className="flex items-center gap-2 text-sm font-medium text-warn">
                      <AlertTriangle size={14} aria-hidden />
                      {t("{{count}} 个用户规则文件加载失败", { count: loadErrors.length })}
                    </p>
                    {loadErrors.map((entry) => (
                      <p key={`${entry.path ?? ""}-${entry.error}`} className="mt-1 font-mono text-xs text-warn">
                        {entry.path ? `${entry.path}: ` : ""}
                        {entry.error}
                      </p>
                    ))}
                  </div>
                ) : null}

                {checks.length === 0 ? (
                  <EmptyState className="py-10" title={t("暂无检测规则")} />
                ) : (
                  <TableWrap>
                    <Table className="min-w-[44rem]">
                      <THead>
                        <TR>
                          <TH>{t("解锁检测")}</TH>
                          <TH className="text-right">{t("证据有效期 (TTL)")}</TH>
                          <TH className="text-right">{t("超时")}</TH>
                          <TH className="text-right">{t("启用")}</TH>
                        </TR>
                      </THead>
                      <TBody>
                        {checks.map((check) => (
                          <CheckRow
                            key={check.id}
                            id={check.id}
                            name={check.name}
                            version={check.version}
                            category={check.category}
                            source={check.source}
                            path={check.path}
                            calibrated={check.calibrated}
                            enabled={check.enabled}
                            enabledOverride={check.enabled_override}
                            ttl={check.ttl}
                            timeout={check.timeout}
                            pending={checkMutation.isPending}
                            onToggle={(enabled) => checkMutation.mutate({ id: check.id, enabled })}
                          />
                        ))}
                      </TBody>
                    </Table>
                  </TableWrap>
                )}
              </Panel>
            ) : null}
          </TabsContent>
        </Tabs>
      </div>

      {editingProvider ? (
        <ProviderEditor
          provider={editingProvider}
          nowNs={providersFetchedAtNs}
          showToast={showToast}
          onSaved={invalidateProviders}
          onClose={() => setEditingId(null)}
        />
      ) : null}

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </section>
  );
}