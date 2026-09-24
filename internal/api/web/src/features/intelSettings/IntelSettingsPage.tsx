import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, KeyRound, RefreshCw, Undo2 } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Switch } from "../../components/ui/Switch";
import { Textarea } from "../../components/ui/Textarea";
import { ToastContainer } from "../../components/ui/Toast";
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

function providerKindVariant(category: string): "info" | "accent" | "neutral" {
  if (category === "online-ip") {
    return "info";
  }
  if (category === "via-node") {
    return "accent";
  }
  return "neutral";
}

// nowNs is the timestamp of the last providers response, taken from the query
// state, so rendering never samples the clock itself.
function ProviderUsageSummary({ provider, nowNs }: { provider: IntelProvider; nowNs: number }) {
  const { t } = useI18n();
  const usage = provider.usage;
  const badges: ReactNode[] = [];
  if (usage.paused) {
    badges.push(
      <Badge key="paused" variant="danger">
        {t("已暂停")} {usage.error_code ? `(${usage.error_code})` : ""}
      </Badge>,
    );
  }
  if (usage.blocked_until_ns > 0 && usage.blocked_until_ns > nowNs) {
    badges.push(
      <Badge key="blocked" variant="warning">
        {t("已限流至 {{time}}", { time: formatNs(usage.blocked_until_ns) })}
      </Badge>,
    );
  }
  if (usage.exhausted) {
    badges.push(
      <Badge key="exhausted" variant="warning">
        {t("今日额度已用尽")}
      </Badge>,
    );
  }
  if (usage.queued > 0 || usage.running > 0 || usage.failed > 0) {
    badges.push(
      <Badge key="queue" variant="neutral">
        {t("队列 {{queued}} · 运行 {{running}} · 失败 {{failed}}", {
          queued: usage.queued,
          running: usage.running,
          failed: usage.failed,
        })}
      </Badge>,
    );
  }
  if (badges.length === 0) {
    badges.push(
      <Badge key="ok" variant="success">
        {t("运行正常")}
      </Badge>,
    );
  }
  return (
    <div style={{ display: "flex", gap: "6px", flexWrap: "wrap", alignItems: "center" }}>
      <span className="muted">
        {t("今日用量 {{used}} / {{limit}}", {
          used: usage.used,
          limit: provider.daily_limit > 0 ? provider.daily_limit : t("不限"),
        })}
      </span>
      {badges}
    </div>
  );
}

function ProviderCard({
  provider,
  nowNs,
  showToast,
  onSaved,
}: {
  provider: IntelProvider;
  nowNs: number;
  showToast: ShowToast;
  onSaved: () => Promise<void>;
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

  return (
    <Card>
      <div className="detail-header">
        <div>
          <h3>
            {provider.name} <span className="muted">{provider.id}</span>
          </h3>
          <p className="muted">{provider.terms}</p>
        </div>
        <div style={{ display: "flex", gap: "8px", alignItems: "center", flexWrap: "wrap" }}>
          <Badge variant={providerKindVariant(provider.category)}>{provider.category}</Badge>
          {provider.via_node ? <Badge variant="accent">{t("经节点查询")}</Badge> : null}
          {provider.direct ? <Badge variant="info">{t("服务器直连查询")}</Badge> : null}
          {provider.requires_key ? (
            <Badge variant={provider.has_key ? "success" : "warning"}>
              {provider.has_key ? t("已配置密钥") : t("缺少密钥")}
            </Badge>
          ) : null}
          <Switch
            checked={provider.enabled}
            aria-label={t("启用数据源 {{id}}", { id: provider.id })}
            title={t("启用数据源 {{id}}", { id: provider.id })}
            disabled={pending}
            onChange={(event) => saveMutation.mutate({ enabled: event.target.checked })}
          />
        </div>
      </div>

      <ProviderUsageSummary provider={provider} nowNs={nowNs} />

      {provider.requires_key && !provider.has_key ? (
        <div className="callout callout-warning" style={{ marginTop: "12px" }}>
          <AlertTriangle size={14} />
          <span>{t("该数据源需要密钥才能运行，配置后会自动启用。")}</span>
        </div>
      ) : null}

      <div className="form-grid" style={{ marginTop: "16px" }}>
        <div className="field-group">
          <label htmlFor={`intel-limit-${provider.id}`}>{t("每日额度")}</label>
          <Input
            id={`intel-limit-${provider.id}`}
            value={dailyLimit}
            inputMode="numeric"
            onChange={(event) => setDailyLimit(event.target.value)}
          />
          <p className="muted">
            {t("0 表示不限；默认 {{def}}", {
              def: provider.default_daily_limit > 0 ? provider.default_daily_limit : t("不限"),
            })}
            {provider.max_daily_limit ? t("，上限 {{max}}", { max: provider.max_daily_limit }) : ""}
          </p>
        </div>
        <div className="field-group">
          <label htmlFor={`intel-qps-${provider.id}`}>{t("每秒请求数 (QPS)")}</label>
          <Input
            id={`intel-qps-${provider.id}`}
            value={qps}
            inputMode="decimal"
            onChange={(event) => setQps(event.target.value)}
          />
          <p className="muted">{t("0 表示不限；默认 {{def}}", { def: provider.default_qps })}</p>
        </div>
        <div className="field-group">
          <label htmlFor={`intel-ttl-${provider.id}`}>{t("证据有效期 (TTL)")}</label>
          <Input
            id={`intel-ttl-${provider.id}`}
            value={ttl}
            placeholder={provider.default_ttl}
            onChange={(event) => setTtl(event.target.value)}
          />
          <p className="muted">{t("Go 时长格式，例如 24h；默认 {{def}}", { def: provider.default_ttl })}</p>
        </div>
      </div>

      {provider.credential_fields?.length ? (
        <p className="muted" style={{ marginTop: "8px" }}>
          {t("该数据源的凭据字段在“配置”中设置：{{fields}}", {
            fields: provider.credential_fields.join(", "),
          })}
        </p>
      ) : null}

      <div className="field-group" style={{ marginTop: "12px" }}>
        <label htmlFor={`intel-config-${provider.id}`}>{t("配置 (JSON)")}</label>
        <Textarea
          id={`intel-config-${provider.id}`}
          rows={3}
          value={configText}
          onChange={(event) => setConfigText(event.target.value)}
        />
        <p className="muted">{t("例如 DNSBL 的 zones 与 resolver；密钥类字段只写入不显示。")}</p>
      </div>

      <div style={{ display: "flex", gap: "8px", flexWrap: "wrap", marginTop: "16px" }}>
        <Button size="sm" onClick={submitSettings} disabled={pending}>
          {saveMutation.isPending ? t("保存中...") : t("保存设置")}
        </Button>
        <Button
          size="sm"
          variant="secondary"
          disabled={pending || apiKey.trim() === ""}
          onClick={() => submitKey(apiKey.trim())}
        >
          <KeyRound size={14} /> {t("保存密钥")}
        </Button>
        <Button size="sm" variant="danger" disabled={pending || !provider.has_key} onClick={() => submitKey("")}>
          {t("清除密钥")}
        </Button>
        {provider.usage.paused || (provider.usage.blocked_until_ns > 0 && provider.usage.blocked_until_ns > nowNs) ? (
          <Button size="sm" variant="secondary" disabled={pending} onClick={() => resumeMutation.mutate()}>
            <Undo2 size={14} /> {t("解除暂停")}
          </Button>
        ) : null}
      </div>

      <div className="field-group" style={{ marginTop: "12px", maxWidth: "420px" }}>
        <label htmlFor={`intel-key-${provider.id}`}>
          {provider.has_key ? t("更新 API Key（留空则保持不变）") : t("API Key")}
        </label>
        <Input
          id={`intel-key-${provider.id}`}
          type="password"
          autoComplete="off"
          value={apiKey}
          placeholder={provider.has_key ? t("已配置") : t("未配置")}
          onChange={(event) => setApiKey(event.target.value)}
        />
        <p className="muted">{t("密钥只写入，接口永不返回；保存后立即对运行中的调度生效。")}</p>
      </div>

      {provider.updated_at_ns > 0 ? (
        <p className="muted" style={{ marginTop: "8px" }}>
          {t("最近修改 {{time}}", { time: formatNs(provider.updated_at_ns) })}
        </p>
      ) : null}
    </Card>
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
  return (
    <div
      style={{
        display: "flex",
        gap: "12px",
        alignItems: "center",
        justifyContent: "space-between",
        flexWrap: "wrap",
        padding: "10px 0",
      }}
    >
      <div>
        <strong>
          {name} <span className="muted">{id}</span>
        </strong>
        <p className="muted">
          {t("版本 {{version}} · 类别 {{category}} · TTL {{ttl}} · 超时 {{timeout}}", {
            version,
            category,
            ttl: formatGoDuration(ttl),
            timeout: formatGoDuration(timeout),
          })}
        </p>
        <p className="muted">
          {path ? t("用户规则文件 {{path}}", { path }) : t("内置规则")}
          {calibrated ? t(" · 校准日期 {{date}}", { date: calibrated }) : ""}
          {enabledOverride ? t(" · 已由设置页覆盖") : ""}
        </p>
      </div>
      <div style={{ display: "flex", gap: "8px", alignItems: "center" }}>
        <Badge variant={source === "user" ? "accent" : "neutral"}>{source}</Badge>
        <Switch
          checked={enabled}
          disabled={pending}
          aria-label={t("启用检测 {{id}}", { id })}
          title={t("启用检测 {{id}}", { id })}
          onChange={(event) => onToggle(event.target.checked)}
        />
      </div>
    </div>
  );
}

export function IntelSettingsPage() {
  const { t } = useI18n();
  const { toasts, showToast, dismissToast } = useToast();
  const queryClient = useQueryClient();
  const [tab, setTab] = useState<"providers" | "checks">("providers");

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

  return (
    <section>
      <div className="section-heading">
        <h2>{t("数据源与解锁检测")}</h2>
        <span>
          {t("{{count}} 个数据源 · {{checks}} 条检测规则", { count: providers.length, checks: checks.length })}
        </span>
        <Button size="sm" variant="secondary" onClick={refresh} disabled={activeQuery.isFetching}>
          <RefreshCw size={14} className={activeQuery.isFetching ? "spin" : undefined} /> {t("刷新")}
        </Button>
      </div>

      <div style={{ display: "flex", gap: "8px", marginBottom: "16px" }}>
        <Button
          size="sm"
          variant={tab === "providers" ? "primary" : "secondary"}
          onClick={() => setTab("providers")}
        >
          {t("数据源")}
        </Button>
        <Button size="sm" variant={tab === "checks" ? "primary" : "secondary"} onClick={() => setTab("checks")}>
          {t("解锁检测")}
        </Button>
      </div>

      {activeQuery.isError ? (
        <Card>
          <div className="callout callout-error">
            <AlertTriangle size={14} />
            <span>{formatApiErrorMessage(activeQuery.error, t)}</span>
          </div>
        </Card>
      ) : null}

      {tab === "providers" ? (
        <div style={{ display: "grid", gap: "16px" }}>
          {providersQuery.isPending ? <Card>{t("正在加载数据源...")}</Card> : null}
          {!providersQuery.isPending && providers.length === 0 ? (
            <Card>
              <div className="empty-box">
                <p>{t("暂无数据源")}</p>
              </div>
            </Card>
          ) : null}
          {providers.map((provider) => (
            <ProviderCard
              key={provider.id}
              provider={provider}
              nowNs={providersFetchedAtNs}
              showToast={showToast}
              onSaved={invalidateProviders}
            />
          ))}
        </div>
      ) : (
        <Card>
          {loadErrors.length > 0 ? (
            <div className="callout callout-warning" style={{ marginBottom: "12px" }}>
              <AlertTriangle size={14} />
              <div>
                <strong>{t("{{count}} 个用户规则文件加载失败", { count: loadErrors.length })}</strong>
                {loadErrors.map((entry) => (
                  <p key={`${entry.path ?? ""}-${entry.error}`} className="muted">
                    {entry.path ? `${entry.path}: ` : ""}
                    {entry.error}
                  </p>
                ))}
              </div>
            </div>
          ) : null}
          {checksQuery.isPending ? <p>{t("正在加载检测规则...")}</p> : null}
          {!checksQuery.isPending && checks.length === 0 ? (
            <div className="empty-box">
              <p>{t("暂无检测规则")}</p>
            </div>
          ) : null}
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
          <p className="muted" style={{ marginTop: "12px" }}>
            {t("用户规则放在 $PRISM_STATE_DIR/checks.d/*.yaml，每 60 秒热加载。")}
          </p>
        </Card>
      )}

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </section>
  );
}
