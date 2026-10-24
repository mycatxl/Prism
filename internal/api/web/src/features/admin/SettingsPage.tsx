import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, RefreshCw, RotateCcw, Save, TriangleAlert, Undo2 } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import {
  CommandSurface,
  ConfirmDrawer,
  ControlButton,
  ControlInput,
  ControlSelect,
  ControlSwitch,
  DetailFact,
  EmptyWorkState,
  PageFrame,
  StatusMark,
  WorkSurface,
} from "../../components/control-plane";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { fetchSession } from "../../lib/api-client";
import { formatApiErrorMessage } from "../../lib/error-message";
import { opsPaths } from "../../lib/paths";
import {
  getDefaultSystemConfig,
  getEnvConfig,
  getSystemConfig,
  patchSystemConfig,
  SYSTEM_CONFIG_DEFAULT_QUERY_KEY,
  SYSTEM_CONFIG_QUERY_KEY,
  SYSTEM_CONFIG_STALE_MS,
  SYSTEM_ENV_CONFIG_QUERY_KEY,
  SYSTEM_ENV_STALE_MS,
} from "../systemConfig/api";
import type { RegistrationMode, RuntimeConfigPatch, SystemConfigView } from "../systemConfig/types";

type SettingsPolicyView = Pick<SystemConfigView, "audit_retention_days" | "usage_retention_days" | "purity_bands" | "registration_mode">;
const MAX_RETENTION_DAYS = 36_500;
const REGISTRATION_MODES = ["off", "invite", "open"] as const;

type Translator = (text: string, values?: Record<string, unknown>) => string;

function isRegistrationMode(value: unknown): value is RegistrationMode {
  return typeof value === "string" && (REGISTRATION_MODES as readonly string[]).includes(value);
}

function draftText(value: number | null | undefined): string {
  return value == null ? "" : String(value);
}

function parseCount(raw: string): number | null {
  const trimmed = raw.trim();
  if (trimmed === "") return 0;
  if (!/^\d+$/.test(trimmed)) return null;
  return Number(trimmed);
}

function modeLabel(mode: RegistrationMode, t: Translator): string {
  if (mode === "open") return t("开放注册");
  if (mode === "invite") return t("仅邀请码");
  return t("关闭注册");
}

function modeTone(mode: RegistrationMode): "signal" | "warn" | "neutral" {
  if (mode === "open") return "warn";
  if (mode === "invite") return "signal";
  return "neutral";
}

export function SettingsPage() {
  const { t } = useI18n();
  const { toasts, showToast, dismissToast } = useToast();
  const configQuery = useQuery({ queryKey: SYSTEM_CONFIG_QUERY_KEY, queryFn: getSystemConfig, staleTime: SYSTEM_CONFIG_STALE_MS });
  const defaultQuery = useQuery({ queryKey: SYSTEM_CONFIG_DEFAULT_QUERY_KEY, queryFn: getDefaultSystemConfig, staleTime: SYSTEM_CONFIG_STALE_MS });
  const envQuery = useQuery({ queryKey: SYSTEM_ENV_CONFIG_QUERY_KEY, queryFn: getEnvConfig, staleTime: SYSTEM_ENV_STALE_MS });
  const [pinnedKey, setPinnedKey] = useState<string | null>(null);
  const liveKeyRef = useRef("");
  const config: SettingsPolicyView = configQuery.data ?? {};
  const defaultConfig: SettingsPolicyView = defaultQuery.data ?? config;
  const envRegistration = typeof envQuery.data?.registration === "string" ? envQuery.data.registration : "";
  const liveKey = `${JSON.stringify(config)}|${envRegistration}`;
  const handleDirtyChange = useCallback((dirty: boolean) => {
    setPinnedKey((previous) => (dirty ? previous ?? liveKeyRef.current : null));
  }, []);

  useEffect(() => {
    liveKeyRef.current = liveKey;
  }, [liveKey]);

  if (configQuery.isLoading || defaultQuery.isLoading || envQuery.isLoading) {
    return (
      <PageFrame eyebrow="Governance / policy" title={t("策略与准入")} description={t("管理账户注册准入、数据保留周期与 IP 纯净度评估阈值。")}>
        <ToastContainer toasts={toasts} onDismiss={dismissToast} />
        <WorkSurface title={t("系统设置")}><div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载系统设置...")}</div></WorkSurface>
      </PageFrame>
    );
  }

  if (configQuery.isError) {
    return (
      <PageFrame eyebrow="Governance / policy" title={t("策略与准入")} description={t("管理账户注册准入、数据保留周期与 IP 纯净度评估阈值。")}>
        <ToastContainer toasts={toasts} onDismiss={dismissToast} />
        <WorkSurface title={t("系统设置")}><EmptyWorkState title={t("系统设置读取失败")} detail={formatApiErrorMessage(configQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void configQuery.refetch()}>{t("重试")}</ControlButton>} /></WorkSurface>
      </PageFrame>
    );
  }

  return (
    <PageFrame
      eyebrow="Governance / policy"
      title={t("策略与准入")}
      description={t("管理账户注册准入、数据保留周期与 IP 纯净度评估阈值。")}
      meta={<StatusMark tone={configQuery.isError ? "warn" : "live"} label={configQuery.isError ? t("数据可能已过期") : t("策略已同步")} />}
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      <SettingsFormContent
        key={pinnedKey ?? liveKey}
        config={config}
        defaultConfig={defaultConfig}
        envRegistration={envRegistration}
        envKnown={!envQuery.isError}
        onDirtyChange={handleDirtyChange}
        showToast={showToast}
        onRefetch={() => { void configQuery.refetch(); void defaultQuery.refetch(); void envQuery.refetch(); }}
      />
    </PageFrame>
  );
}

function SettingsFormContent({
  config,
  defaultConfig,
  envRegistration,
  envKnown,
  onDirtyChange,
  onRefetch,
  showToast,
}: {
  config: SettingsPolicyView;
  defaultConfig: SettingsPolicyView;
  envRegistration: string;
  envKnown: boolean;
  onDirtyChange: (dirty: boolean) => void;
  onRefetch: () => void;
  showToast: (tone: "success" | "error", text: string) => void;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const savedOverride: RegistrationMode | null = isRegistrationMode(config.registration_mode) ? config.registration_mode : null;
  const envMode: RegistrationMode | null = isRegistrationMode(envRegistration) ? envRegistration : null;
  const effectiveMode: RegistrationMode = savedOverride ?? envMode ?? "off";
  const [mode, setMode] = useState<RegistrationMode>(effectiveMode);
  const [followEnv, setFollowEnv] = useState(false);
  const [auditRetention, setAuditRetention] = useState(draftText(config.audit_retention_days));
  const [usageRetention, setUsageRetention] = useState(draftText(config.usage_retention_days));
  const [purityExcellent, setPurityExcellent] = useState(draftText(config.purity_bands?.excellent));
  const [purityClean, setPurityClean] = useState(draftText(config.purity_bands?.clean));
  const [purityFair, setPurityFair] = useState(draftText(config.purity_bands?.fair));
  const [purityMixed, setPurityMixed] = useState(draftText(config.purity_bands?.mixed));
  const [resetOpen, setResetOpen] = useState(false);
  const [errorMsg, setErrorMsg] = useState("");

  const updateMutation = useMutation({
    mutationFn: (patch: RuntimeConfigPatch) => patchSystemConfig(patch),
    onSuccess: async (_data, variables) => {
      setErrorMsg("");
      showToast("success", t("系统设置已更新"));
      await queryClient.invalidateQueries({ queryKey: SYSTEM_CONFIG_QUERY_KEY });
      if (variables.registration_mode !== undefined) await fetchSession().catch(() => undefined);
    },
    onError: (error) => {
      const message = formatApiErrorMessage(error, t);
      setErrorMsg(message);
      showToast("error", message);
    },
  });

  const auditValue = parseCount(auditRetention);
  const usageValue = parseCount(usageRetention);
  const retentionError = t("必须是 0 到 {{max}} 之间的整数。", { max: MAX_RETENTION_DAYS });
  const auditError = auditValue === null || auditValue > MAX_RETENTION_DAYS ? retentionError : "";
  const usageError = usageValue === null || usageValue > MAX_RETENTION_DAYS ? retentionError : "";
  const purityValues = [parseCount(purityExcellent), parseCount(purityClean), parseCount(purityFair), parseCount(purityMixed)];
  const purityBlank = [purityExcellent, purityClean, purityFair, purityMixed].every((value) => value.trim() === "");
  const purityParsed = purityValues.every((value) => value !== null);
  const purityInRange = purityParsed && purityValues.every((value) => (value as number) >= 1 && (value as number) <= 100);
  const [excellent, clean, fair, mixed] = purityValues.map((value) => value ?? 0);
  const purityOrdered = purityInRange && mixed < fair && fair < clean && clean < excellent;
  const purityNotConfigured = config.purity_bands == null && purityBlank;
  const purityError = !purityNotConfigured && (!purityInRange || !purityOrdered) ? t("阈值必须是 1 到 100 之间的整数，并从高到低：极优 > 优良 > 一般 > 混合。") : "";

  const patch: RuntimeConfigPatch = {};
  if (auditValue !== null && !auditError && auditValue !== (config.audit_retention_days ?? 0)) patch.audit_retention_days = auditValue;
  if (usageValue !== null && !usageError && usageValue !== (config.usage_retention_days ?? 0)) patch.usage_retention_days = usageValue;
  if (purityOrdered && (config.purity_bands == null || excellent !== config.purity_bands.excellent || clean !== config.purity_bands.clean || fair !== config.purity_bands.fair || mixed !== config.purity_bands.mixed)) {
    patch.purity_bands = { excellent, clean, fair, mixed };
  }
  if (followEnv && savedOverride !== null) patch.registration_mode = "";
  else if (!followEnv && mode !== effectiveMode) patch.registration_mode = mode;
  const changedSections = [patch.registration_mode !== undefined ? t("注册准入") : null, patch.audit_retention_days !== undefined || patch.usage_retention_days !== undefined ? t("数据保留") : null, patch.purity_bands !== undefined ? t("纯净度阈值") : null].filter((value): value is string => Boolean(value));
  const changedSectionsText = new Intl.ListFormat(isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN", { type: "conjunction" }).format(changedSections);
  const isDirty = Object.keys(patch).length > 0;

  useEffect(() => { onDirtyChange(isDirty); }, [isDirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange(false), [onDirtyChange]);

  const revertChanges = () => {
    setAuditRetention(draftText(config.audit_retention_days));
    setUsageRetention(draftText(config.usage_retention_days));
    setPurityExcellent(draftText(config.purity_bands?.excellent));
    setPurityClean(draftText(config.purity_bands?.clean));
    setPurityFair(draftText(config.purity_bands?.fair));
    setPurityMixed(draftText(config.purity_bands?.mixed));
    setMode(effectiveMode);
    setFollowEnv(false);
    setErrorMsg("");
  };

  const handleResetToDefault = () => {
    updateMutation.mutate({ audit_retention_days: defaultConfig.audit_retention_days, usage_retention_days: defaultConfig.usage_retention_days, purity_bands: defaultConfig.purity_bands }, { onSuccess: () => setResetOpen(false) });
  };

  return (
    <div className="cp-stack">
      <CommandSurface actions={<><ControlButton variant="secondary" size="sm" onPress={onRefetch} isDisabled={updateMutation.isPending}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton><ControlButton variant="secondary" size="sm" onPress={() => setResetOpen(true)} isDisabled={updateMutation.isPending}><RotateCcw size={14} aria-hidden />{t("恢复默认")}</ControlButton><ControlButton variant="primary" size="sm" onPress={() => updateMutation.mutate(patch)} isDisabled={!isDirty || Boolean(auditError || usageError || purityError) || updateMutation.isPending} loading={updateMutation.isPending}><Save size={14} aria-hidden />{t("保存更改")}</ControlButton></>}>
        <StatusMark tone={isDirty ? "warn" : "neutral"} label={isDirty ? t("有未保存的更改") : t("当前无未保存改动")} />
      </CommandSurface>

      {errorMsg ? <p className="cp-notice cp-notice--alert" role="alert">{errorMsg}</p> : null}
      {isDirty ? <div className="cp-notice cp-notice--warn"><span><strong>{t("有未保存的更改")}</strong>{changedSectionsText ? ` · ${changedSectionsText}` : ""}</span><ControlButton variant="ghost" size="sm" onPress={revertChanges} isDisabled={updateMutation.isPending}><Undo2 size={13} aria-hidden />{t("放弃更改")}</ControlButton></div> : null}

      <WorkSurface eyebrow="Governance / registration" title={t("账户注册")} description={t("决定访客能否自行创建账号。保存后立即生效，无需重启服务。")} meta={envKnown ? <StatusMark tone={modeTone(effectiveMode)} label={modeLabel(effectiveMode, t)} /> : <StatusMark tone="warn" label={t("部署环境未知")} />}>
        {envKnown ? <div className="cp-stack">
          <dl className="cp-detail-facts"><DetailFact label={t("当前生效策略")} value={<StatusMark tone={modeTone(effectiveMode)} label={modeLabel(effectiveMode, t)} />} /><DetailFact label={t("策略来源")} value={savedOverride ? t("面板设置") : envMode ? t("部署环境") : t("未配置")} /></dl>
          <ControlSelect label={t("注册准入")} hint={t("这是对外的注册入口，与管理员在控制台里创建账号无关。")} value={followEnv ? envMode ?? "off" : mode} disabled={followEnv} onChange={(event) => { if (isRegistrationMode(event.target.value)) { setMode(event.target.value); setFollowEnv(false); } }}>
            <option value="off">{t("关闭注册")}</option><option value="invite">{t("仅邀请码")}</option><option value="open">{t("开放注册")}</option>
          </ControlSelect>
          {savedOverride !== null ? <ControlSwitch label={t("跟随部署环境") } hint={t("移除面板覆盖后，注册策略回到 PRISM_REGISTRATION。") } isSelected={followEnv} onChange={(value) => { setFollowEnv(value); if (value) setMode(envMode ?? "off"); }} /> : null}
          {mode === "invite" && !followEnv ? <div className="cp-inset-stack"><span>{t("邀请码在邀请管理里创建，可以预先绑定套餐。")}</span><Link to={opsPaths.invites} className="link-quiet"><ArrowUpRight size={13} aria-hidden />{t("管理邀请码")}</Link></div> : null}
          {mode === "open" && !followEnv ? <p className="cp-notice cp-notice--warn"><TriangleAlert size={14} aria-hidden />{t("开放注册没有任何准入校验，建议同时在反向代理上限制注册请求频率。")}</p> : null}
        </div> : <EmptyWorkState title={t("无法读取部署环境配置")} detail={t("暂时无法显示注册准入的当前状态。") } action={<ControlButton variant="secondary" size="sm" onPress={onRefetch}>{t("重试")}</ControlButton>} />}
      </WorkSurface>

      <WorkSurface eyebrow="Governance / retention" title={t("数据保留周期")} description={t("设置审计日志与用量统计数据的自动清理周期，0 或留空表示使用出厂窗口。")}>
        <div className="cp-form-grid">
          <ControlInput label={t("审计日志保留天数")} hint={auditError || (defaultConfig.audit_retention_days != null ? t("默认值: {{val}} 天", { val: defaultConfig.audit_retention_days }) : t("未设置"))} type="number" min="0" max={MAX_RETENTION_DAYS} step="1" value={auditRetention} onChange={(event) => setAuditRetention(event.target.value)} />
          <ControlInput label={t("用量记录保留天数")} hint={usageError || (defaultConfig.usage_retention_days != null ? t("默认值: {{val}} 天", { val: defaultConfig.usage_retention_days }) : t("未设置"))} type="number" min="0" max={MAX_RETENTION_DAYS} step="1" value={usageRetention} onChange={(event) => setUsageRetention(event.target.value)} />
        </div>
      </WorkSurface>

      <WorkSurface eyebrow="Governance / purity policy" title={t("IP 纯净度等级分段")} description={t("配置网络节点 IP 纯净度评估的四个等级阈值（1 - 100 分）。")}>
        <div className="cp-form-grid">
          <ControlInput label={t("极优阈值 (Excellent)")} hint={config.purity_bands?.excellent == null ? t("未设置") : t("已配置")} type="number" min="1" max="100" step="1" value={purityExcellent} onChange={(event) => setPurityExcellent(event.target.value)} />
          <ControlInput label={t("优良阈值 (Clean)")} hint={config.purity_bands?.clean == null ? t("未设置") : t("已配置")} type="number" min="1" max="100" step="1" value={purityClean} onChange={(event) => setPurityClean(event.target.value)} />
          <ControlInput label={t("一般阈值 (Fair)")} hint={config.purity_bands?.fair == null ? t("未设置") : t("已配置")} type="number" min="1" max="100" step="1" value={purityFair} onChange={(event) => setPurityFair(event.target.value)} />
          <ControlInput label={t("混合阈值 (Mixed)")} hint={config.purity_bands?.mixed == null ? t("未设置") : t("已配置")} type="number" min="1" max="100" step="1" value={purityMixed} onChange={(event) => setPurityMixed(event.target.value)} />
        </div>
        {purityError ? <p className="cp-form-error" role="alert">{purityError}</p> : null}
      </WorkSurface>

      <ConfirmDrawer open={resetOpen} onOpenChange={setResetOpen} title={t("确认恢复默认设置")} eyebrow="Governance / reset" message={t("确定要将数据保留周期和纯净度等级阈值恢复为系统默认配置吗？未保存的手动更改将丢失。")} cancelLabel={t("取消")} confirmLabel={t("确认恢复")} loading={updateMutation.isPending} onConfirm={handleResetToDefault} />
    </div>
  );
}

export default SettingsPage;
