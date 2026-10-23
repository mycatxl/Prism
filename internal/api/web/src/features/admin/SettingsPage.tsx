import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowUpRight,
  RefreshCw,
  RotateCcw,
  Save,
  TriangleAlert,
  Undo2,
} from "lucide-react";
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import {
  DialogContent,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
} from "../../components/ui/Dialog";
import { Fieldset, Input } from "../../components/ui/Input";
import { PageHeader } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelHeader } from "../../components/ui/Panel";
import { ErrorState, LoadingState } from "../../components/ui/QueryState";
import { RadioGroup } from "../../components/ui/RadioGroup";
import { StatusMark, type StatusMarkTone } from "../../components/ui/StatusMark";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
// The section list in the dirty banner is joined with the session's own list
// separators, so the locale helpers ride along with the rest of i18n.
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { fetchSession } from "../../lib/api-client";
import { formatApiErrorMessage } from "../../lib/error-message";
import { opsPaths } from "../../lib/paths";
// The canonical client for these endpoints lives in the system config feature:
// the console writes and reads the same two urls the system config page does, so
// they share its cache keys instead of keeping a second copy of the same GET.
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

/*
 * The slice of the shared system config this page reads. GET /api/v1/system/config
 * answers one object; the runtime form edits its runtime fields while this page
 * edits the policy fields beside them, so the two share the type - and, because
 * of that, the cache entry - without either pretending to own the whole thing.
 */
type SettingsPolicyView = Pick<
  SystemConfigView,
  "audit_retention_days" | "usage_retention_days" | "purity_bands" | "registration_mode"
>;

// MAX_RETENTION_DAYS mirrors config.MaxRetentionDays on the server: a longer
// window overflows the nanosecond offset the prune subtracts from now, which
// moves the cutoff into the future and deletes the whole table.
const MAX_RETENTION_DAYS = 36500;

// The three policies the register endpoint understands. Kept as a literal tuple
// so an unknown value from either source resolves to "not a policy" instead of
// being rendered as one.
const REGISTRATION_MODES = ["off", "invite", "open"] as const;

function isRegistrationMode(value: unknown): value is RegistrationMode {
  return typeof value === "string" && (REGISTRATION_MODES as readonly string[]).includes(value);
}

function modeTone(mode: RegistrationMode): StatusMarkTone {
  // Open sign-up is the state that needs an eye on it, so it carries the caution
  // tone; closed is the quiet default. The word is always there as well, so the
  // colour is never the only thing that says which policy is in force.
  if (mode === "open") return "warn";
  if (mode === "invite") return "signal";
  return "neutral";
}

/**
 * A section header's state: the canonical dot-plus-word, never a badge stamp.
 * `warn` marks a section carrying unsaved edits — the accent colour is the
 * interaction colour, and a state is not an interaction.
 */
function SectionStateMark({ tone, label }: { tone: "warn" | "neutral"; label: string }) {
  return (
    <span className="flex items-center gap-2">
      <span aria-hidden className={tone === "warn" ? "state-dot state-dot--warn" : "state-dot"} />
      <span className="state-text">{label}</span>
    </span>
  );
}

/**
 * The draft text for an optional config number: empty when the server does not
 * carry one. An empty field reads as "not set" — the shipped window still
 * applies — so the form never puts a built-in fallback on the sheet as if the
 * deployment had set it.
 */
function draftText(value: number | null | undefined): string {
  return value == null ? "" : String(value);
}

/** The placeholder for the same field: the shipped default, when it is known. */
function defaultText(value: number | null | undefined): string | undefined {
  return value == null ? undefined : String(value);
}

/**
 * Parses a whole, non-negative count. Empty means "use the shipped window" and
 * reads as 0, which is what the server does with it; anything that is not a
 * plain number is rejected instead of silently becoming 0, because a typo that
 * saves as "no retention" is a data-loss bug wearing a form field's clothes.
 */
function parseCount(raw: string): number | null {
  const trimmed = raw.trim();
  if (trimmed === "") return 0;
  if (!/^\d+$/.test(trimmed)) return null;
  return Number(trimmed);
}

export function SettingsPage() {
  const { t } = useI18n();
  /*
   * The toast lives above the form, not inside it. Saving changes the config,
   * the config signature is the form's key, and the remount that follows would
   * unmount the toast along with the form — a confirmation nobody ever sees.
   */
  const { toasts, showToast, dismissToast } = useToast();

  // Same keys, same query functions and same windows as the system config page
  // and the shell: this page edits two slices of one object, and a key of its own
  // made the console fetch the same url again while a save here left the copies
  // other pages were reading untouched.
  const configQuery = useQuery({
    queryKey: SYSTEM_CONFIG_QUERY_KEY,
    queryFn: getSystemConfig,
    staleTime: SYSTEM_CONFIG_STALE_MS,
  });

  const defaultQuery = useQuery({
    queryKey: SYSTEM_CONFIG_DEFAULT_QUERY_KEY,
    queryFn: getDefaultSystemConfig,
    staleTime: SYSTEM_CONFIG_STALE_MS,
  });

  /*
   * The deployment's own policy. The panel can override it, but it also has to
   * be able to name what it is overriding and what clearing the override
   * returns to, and that value is not in the runtime config.
   */
  const envQuery = useQuery({
    queryKey: SYSTEM_ENV_CONFIG_QUERY_KEY,
    queryFn: getEnvConfig,
    staleTime: SYSTEM_ENV_STALE_MS,
  });

  /*
   * No fallback object here. Every policy field is optional on the wire, and
   * answering a missing one with the shipped number would render a default as
   * if the deployment had set it — the form reads absent fields as "not set"
   * instead. `{}` is only the shape the optional fields allow.
   */
  const config: SettingsPolicyView = configQuery.data ?? {};
  const defaultConfig = defaultQuery.data ?? config;
  const envRegistration =
    typeof envQuery.data?.registration === "string" ? envQuery.data.registration : "";

  /*
   * The form is keyed on the *content* of the config, not on when it was last
   * fetched. `dataUpdatedAt` changes on every background refetch — and the query
   * client refetches on window focus — so keying on it remounted this form and
   * threw away whatever the operator had typed without saving.
   *
   * The inherited registration policy is part of the key for the same reason in
   * the other direction: the radios start from the value that is actually in
   * force, so their initial state must be rebuilt once the deployment's policy
   * arrives rather than left on a placeholder.
   *
   * A refetch can also land *while* the operator has unsaved edits — another
   * page saves the shared runtime slice, the signature moves, and the remount
   * drops them. While the form is dirty the key is pinned to the signature it
   * mounted with; the new one is adopted only once the edits are saved or
   * dropped. The form reports that through `onDirtyChange`.
   */
  const [pinnedKey, setPinnedKey] = useState<string | null>(null);
  const liveKeyRef = useRef("");
  const handleDirtyChange = useCallback((dirty: boolean) => {
    setPinnedKey((previous) => (dirty ? previous ?? liveKeyRef.current : null));
  }, []);
  const liveKey = `${JSON.stringify(config)}|${envRegistration}`;

  // Kept current in an effect rather than a render-phase write: the pin has to
  // name the signature that was live when the edits started, and effects run
  // after the commit they describe.
  useEffect(() => {
    liveKeyRef.current = liveKey;
  });

  if (configQuery.isLoading || defaultQuery.isLoading || envQuery.isLoading) {
    return (
      <div className="space-y-6">
        <PageHeader
          title={t("策略与准入")}
          description={t("管理账户注册准入、数据保留周期与 IP 纯净度评估阈值。")}
        />
        <Panel>
          <PanelBody>
            <LoadingState label={t("正在加载系统设置...")} className="py-16" />
          </PanelBody>
        </Panel>
      </div>
    );
  }

  if (configQuery.isError) {
    return (
      <div className="space-y-6">
        <PageHeader
          title={t("策略与准入")}
          description={t("管理账户注册准入、数据保留周期与 IP 纯净度评估阈值。")}
        />
        <Panel>
          <PanelBody>
            <ErrorState
              message={formatApiErrorMessage(configQuery.error, t)}
              onRetry={() => void configQuery.refetch()}
              className="py-16"
            />
          </PanelBody>
        </Panel>
      </div>
    );
  }

  return (
    <>
      <SettingsFormContent
        key={pinnedKey ?? liveKey}
        config={config}
        defaultConfig={defaultConfig}
        envRegistration={envRegistration}
        envKnown={!envQuery.isError}
        onDirtyChange={handleDirtyChange}
        showToast={showToast}
        onRefetch={() => {
          void configQuery.refetch();
          void defaultQuery.refetch();
          void envQuery.refetch();
        }}
      />
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </>
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
  const auditFieldId = useId();
  const usageFieldId = useId();
  const excellentFieldId = useId();
  const cleanFieldId = useId();
  const fairFieldId = useId();
  const mixedFieldId = useId();
  // Field-level errors get their own targets so each Input's aria-describedby
  // names exactly the message that reports on it; the dialog's body paragraph
  // is the description Radix's content points at.
  const auditErrorId = useId();
  const usageErrorId = useId();
  const purityErrorId = useId();
  const resetDescriptionId = useId();

  /*
   * The override the server holds, if any, and the policy the deployment would
   * apply without it. `""` means "no override" - it is a real value the endpoint
   * accepts, and it is how the panel hands the decision back to the environment.
   */
  const savedOverride: RegistrationMode | null = isRegistrationMode(config.registration_mode)
    ? config.registration_mode
    : null;
  const envMode: RegistrationMode | null = isRegistrationMode(envRegistration)
    ? envRegistration
    : null;
  const effectiveMode: RegistrationMode = savedOverride ?? envMode ?? "off";

  const [mode, setMode] = useState<RegistrationMode>(effectiveMode);
  const [followEnv, setFollowEnv] = useState(false);

  // Optional policy numbers start empty when the server does not carry them:
  // an empty field is "not set" (the shipped window still applies), and
  // pre-filling the shipped number would dress a default up as a deployed
  // value the API never reported.
  const [auditRetention, setAuditRetention] = useState(draftText(config.audit_retention_days));
  const [usageRetention, setUsageRetention] = useState(draftText(config.usage_retention_days));
  const [purityExcellent, setPurityExcellent] = useState(draftText(config.purity_bands?.excellent));
  const [purityClean, setPurityClean] = useState(draftText(config.purity_bands?.clean));
  const [purityFair, setPurityFair] = useState(draftText(config.purity_bands?.fair));
  const [purityMixed, setPurityMixed] = useState(draftText(config.purity_bands?.mixed));

  const [resetModalOpen, setResetModalOpen] = useState(false);
  const [errorMsg, setErrorMsg] = useState("");

  const updateMutation = useMutation({
    mutationFn: (patch: RuntimeConfigPatch) => patchSystemConfig(patch),
    onSuccess: (_data, variables) => {
      showToast("success", t("系统设置已更新"));
      setErrorMsg("");
      // The one shared entry: the system config page, the request log and the
      // shell all read this url, and the save has to reach every one of them.
      void queryClient.invalidateQueries({ queryKey: SYSTEM_CONFIG_QUERY_KEY });
      // The console reads the sign-up policy from its own session; without this
      // the page keeps advertising the previous door until the next reload.
      if (variables.registration_mode !== undefined) {
        void fetchSession().catch(() => undefined);
      }
    },
    onError: (err) => {
      const msg = formatApiErrorMessage(err, t);
      setErrorMsg(msg);
      showToast("error", msg);
    },
  });

  // --- validation -----------------------------------------------------------

  const auditValue = parseCount(auditRetention);
  const usageValue = parseCount(usageRetention);
  const auditOutOfRange = auditValue !== null && auditValue > MAX_RETENTION_DAYS;
  const usageOutOfRange = usageValue !== null && usageValue > MAX_RETENTION_DAYS;
  const retentionErrorText = t("必须是 0 到 {{max}} 之间的整数。", {
    max: MAX_RETENTION_DAYS,
  });
  const auditError =
    auditValue === null || auditOutOfRange ? retentionErrorText : undefined;
  const usageError =
    usageValue === null || usageOutOfRange ? retentionErrorText : undefined;

  const purityValues = [
    parseCount(purityExcellent),
    parseCount(purityClean),
    parseCount(purityFair),
    parseCount(purityMixed),
  ];
  const purityParsed = purityValues.every((value) => value !== null);
  const purityInRange =
    purityParsed && purityValues.every((value) => (value as number) >= 1 && (value as number) <= 100);
  // The server's Normalized() silently replaces an out-of-order set with the
  // shipped thresholds, so an off-by-one here would look like the save worked
  // and then quietly do something else. Check it before it leaves the page.
  const [excellent, clean, fair, mixed] = purityValues.map((value) => value ?? 0);
  /*
   * A config without purity bands plus an untouched form is "not configured",
   * not a validation failure: the shipped thresholds apply, and demanding four
   * numbers the server never sent would load the page looking broken.
   */
  const purityBlank =
    purityExcellent.trim() === "" &&
    purityClean.trim() === "" &&
    purityFair.trim() === "" &&
    purityMixed.trim() === "";
  const purityNotConfigured = config.purity_bands == null && purityBlank;
  const purityOrdered =
    purityInRange && mixed < fair && fair < clean && clean < excellent;
  const purityError =
    !purityNotConfigured && (!purityInRange || !purityOrdered)
      ? t("阈值必须是 1 到 100 之间的整数，并从高到低：极优 > 优良 > 一般 > 混合。")
      : undefined;

  // --- dirty state ----------------------------------------------------------

  /*
   * An absent window behaves as 0 — both mean "use the shipped window", so the
   * comparison may treat "unset" and "cleared" as the same state without
   * inventing a number. The bands have no such equivalence: any complete set is
   * a change when the server carries none.
   */
  const auditDirty =
    auditValue !== null &&
    !auditOutOfRange &&
    auditValue !== (config.audit_retention_days ?? 0);
  const usageDirty =
    usageValue !== null &&
    !usageOutOfRange &&
    usageValue !== (config.usage_retention_days ?? 0);
  const purityDirty =
    purityOrdered &&
    (config.purity_bands == null ||
      excellent !== config.purity_bands.excellent ||
      clean !== config.purity_bands.clean ||
      fair !== config.purity_bands.fair ||
      mixed !== config.purity_bands.mixed);

  /*
   * What the save actually sends: only the fields that changed. The endpoint
   * rejects unknown fields outright and treats absent ones as "leave it", so a
   * patch built from the whole form would rewrite settings this page never
   * showed — and would pin a registration override the operator never asked for.
   */
  const patch: RuntimeConfigPatch = {};
  if (auditDirty) patch.audit_retention_days = auditValue as number;
  if (usageDirty) patch.usage_retention_days = usageValue as number;
  if (purityDirty) {
    patch.purity_bands = { excellent, clean, fair, mixed };
  }
  if (followEnv) {
    // Clearing is only a change when there is an override to clear.
    if (savedOverride !== null) patch.registration_mode = "";
  } else if (mode !== effectiveMode) {
    patch.registration_mode = mode;
  }

  const registrationDirty = patch.registration_mode !== undefined;
  const dirtyKeys = Object.keys(patch);
  const isDirty = dirtyKeys.length > 0;
  const hasErrors = Boolean(auditError || usageError || purityError);
  const canSave = isDirty && !hasErrors && !updateMutation.isPending;

  // The parent pins the form key while this form is dirty, so a background
  // refetch cannot remount it out from under unsaved edits (see SettingsPage).
  useEffect(() => {
    onDirtyChange(isDirty);
  }, [isDirty, onDirtyChange]);

  const changedSections = [
    registrationDirty ? t("注册准入") : null,
    auditDirty || usageDirty ? t("数据保留") : null,
    purityDirty ? t("纯净度阈值") : null,
  ].filter((label): label is string => label !== null);

  /*
   * Joined the way the session's locale joins lists, instead of a hardcoded 、
   * that reads as a typo in an English session.
   */
  const changedSectionsText = new Intl.ListFormat(
    isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN",
    { type: "conjunction" },
  ).format(changedSections);

  const handleSave = () => {
    if (!canSave) return;
    setErrorMsg("");
    updateMutation.mutate(patch);
  };

  const revertChanges = () => {
    // Same source as the draft init: an absent field reverts to empty ("not
    // set"), never to a shipped number posed as the current value.
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
    updateMutation.mutate(
      {
        audit_retention_days: defaultConfig.audit_retention_days,
        usage_retention_days: defaultConfig.usage_retention_days,
        purity_bands: defaultConfig.purity_bands,
      },
      {
        onSuccess: () => {
          setResetModalOpen(false);
          showToast("success", t("已恢复为默认设置"));
        },
      },
    );
  };

  const modeLabel = (value: RegistrationMode) => {
    if (value === "open") return t("开放注册");
    if (value === "invite") return t("仅邀请码");
    return t("关闭注册");
  };

  const sourceLabel = savedOverride ? t("面板设置") : envMode ? t("部署环境") : t("未配置");

  const sourceSentence = savedOverride
    ? t("面板正在覆盖部署环境，保存后立刻作用于新的注册请求。")
    : envMode
      ? t("由部署环境变量 PRISM_REGISTRATION={{mode}} 决定，面板没有覆盖。", {
          mode: envMode,
        })
      : t("部署环境没有配置注册策略，当前按关闭处理。");

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("策略与准入")}
        description={t("管理账户注册准入、数据保留周期与 IP 纯净度评估阈值。")}
        actions={
          <div className="flex items-center gap-2">
            <Button
              variant="secondary"
              onClick={onRefetch}
              disabled={updateMutation.isPending}
              title={t("刷新")}
            >
              <RefreshCw size={14} />
              <span>{t("刷新")}</span>
            </Button>
            <Button
              variant="secondary"
              onClick={() => setResetModalOpen(true)}
              disabled={updateMutation.isPending}
            >
              <RotateCcw size={14} />
              <span>{t("恢复默认")}</span>
            </Button>
            <Button
              variant="primary"
              onClick={handleSave}
              disabled={!canSave}
              loading={updateMutation.isPending}
            >
              <Save size={14} />
              <span>{t("保存更改")}</span>
            </Button>
          </div>
        }
      />

      {errorMsg && (
        <div
          role="alert"
          className="rounded-control border border-alert/30 bg-alert/10 px-4 py-3 text-xs text-alert"
        >
          {errorMsg}
        </div>
      )}

      {isDirty && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-control border border-warn/30 bg-warn-wash px-3.5 py-2.5">
          <span className="text-xs font-medium text-warn">{t("有未保存的更改")}</span>
          <span className="text-xs text-ink-soft">{changedSectionsText}</span>
          <Button
            variant="quiet"
            size="sm"
            className="ml-auto"
            onClick={revertChanges}
            disabled={updateMutation.isPending}
          >
            <Undo2 size={13} aria-hidden />
            {t("放弃更改")}
          </Button>
        </div>
      )}

      {/* Sign-up admission: the control that decides whether the door exists. */}
      <Panel>
        <PanelHeader
          title={t("账户注册")}
          description={t("决定访客能否自行创建账号。保存后立即生效，无需重启服务。")}
          meta={
            registrationDirty ? (
              <SectionStateMark tone="warn" label={t("已修改")} />
            ) : undefined
          }
        />
        <PanelBody className="space-y-5">
          {envKnown ? (
            <>
              <div className="flex flex-col gap-3 rounded-control border border-rule bg-paper-inset px-3.5 py-3 sm:flex-row sm:items-start sm:justify-between">
                <div className="min-w-0 space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <StatusMark tone={modeTone(effectiveMode)} label={modeLabel(effectiveMode)} />
                    <Badge tone="outline">{sourceLabel}</Badge>
                  </div>
                  <p className="max-w-[68ch] text-xs leading-relaxed text-ink-faint">
                    {sourceSentence}
                  </p>
                </div>
                {savedOverride !== null && !followEnv && (
                  <Button
                    variant="quiet"
                    size="sm"
                    onClick={() => {
                      setFollowEnv(true);
                      setMode(envMode ?? "off");
                    }}
                    disabled={updateMutation.isPending}
                  >
                    {t("改回跟随部署环境")}
                  </Button>
                )}
              </div>

              <RadioGroup
                legend={t("注册准入")}
                hint={t("这是对外的注册入口，与管理员在控制台里创建账号无关。")}
                value={mode}
                onValueChange={(next) => {
                  if (!isRegistrationMode(next)) return;
                  setMode(next);
                  setFollowEnv(false);
                }}
                options={[
                  {
                    value: "off",
                    label: t("关闭注册"),
                    description: t(
                      "注册接口直接拒绝请求，登录页也不显示注册入口。只有管理员能创建账号。",
                    ),
                  },
                  {
                    value: "invite",
                    label: t("仅邀请码"),
                    description: t(
                      "注册必须提供有效的邀请码，邀请码决定新账号绑定的套餐。",
                    ),
                  },
                  {
                    value: "open",
                    label: t("开放注册"),
                    description: t(
                      "任何人都能自助注册。新账号不绑定套餐，需要管理员另行分配。",
                    ),
                  },
                ]}
              />

              {followEnv ? (
                <p className="text-xs leading-relaxed text-ink-soft">
                  {t(
                    "保存后将移除面板覆盖，注册行为回到部署环境（PRISM_REGISTRATION={{mode}}）。",
                    { mode: envMode ?? "off" },
                  )}
                </p>
              ) : mode === "invite" ? (
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-control border border-rule bg-paper-inset px-3.5 py-2.5 text-xs text-ink-soft">
                  <span>{t("邀请码在邀请管理里创建，可以预先绑定套餐。")}</span>
                  <Link
                    to={opsPaths.invites}
                    className="action inline-flex items-center gap-1 font-medium text-accent hover:text-accent-deep"
                  >
                    {t("管理邀请码")}
                    <ArrowUpRight size={12} aria-hidden />
                  </Link>
                </div>
              ) : mode === "open" ? (
                <div className="flex items-start gap-2.5 rounded-control border border-warn/30 bg-warn-wash px-3.5 py-2.5 text-xs text-warn">
                  <TriangleAlert size={14} aria-hidden className="mt-0.5 shrink-0" />
                  <p className="max-w-[68ch] leading-relaxed">
                    {t(
                      "开放注册没有任何准入校验，注册接口也不限速。建议同时在反向代理上限制 /api/v1/auth/register 的请求频率。",
                    )}
                  </p>
                </div>
              ) : null}
            </>
          ) : (
            <ErrorState
              message={t("无法读取部署环境配置，暂时无法显示注册准入的当前状态。")}
              onRetry={onRefetch}
            />
          )}
        </PanelBody>
      </Panel>

      {/* Retention Section */}
      <Panel>
        <PanelHeader
          title={t("数据保留周期")}
          description={t("设置审计日志与用量统计数据的自动清理周期，0 或留空表示使用出厂窗口。")}
          meta={
            auditDirty || usageDirty ? (
              <SectionStateMark tone="warn" label={t("已修改")} />
            ) : (
              <SectionStateMark tone="neutral" label={t("系统默认")} />
            )
          }
        />
        <PanelBody className="space-y-6">
          <div className="grid grid-cols-1 gap-6 sm:grid-cols-2">
            <Fieldset
              label={t("审计日志保留天数")}
              hint={t("超过设定天数的审计日志将被自动清理。")}
              htmlFor={auditFieldId}
            >
              <Input
                id={auditFieldId}
                type="number"
                min="0"
                max={MAX_RETENTION_DAYS}
                step="1"
                value={auditRetention}
                onChange={(e) => setAuditRetention(e.target.value)}
                placeholder={defaultText(defaultConfig.audit_retention_days)}
                invalid={Boolean(auditError)}
                aria-describedby={auditError ? auditErrorId : undefined}
              />
              {auditError ? (
                <p id={auditErrorId} role="alert" className="text-xs text-alert">
                  {auditError}
                </p>
              ) : config.audit_retention_days == null ? (
                <p className="text-xs text-ink-faint">
                  {defaultConfig.audit_retention_days != null
                    ? t("未设置（默认 {{val}} 天）", { val: defaultConfig.audit_retention_days })
                    : t("未设置")}
                </p>
              ) : (
                defaultConfig.audit_retention_days != null && (
                  <p className="text-xs text-ink-faint">
                    {t("默认值: {{val}} 天", { val: defaultConfig.audit_retention_days })}
                  </p>
                )
              )}
            </Fieldset>

            <Fieldset
              label={t("用量记录保留天数")}
              hint={t("超过设定天数的每日用量统计数据将被自动清理。")}
              htmlFor={usageFieldId}
            >
              <Input
                id={usageFieldId}
                type="number"
                min="0"
                max={MAX_RETENTION_DAYS}
                step="1"
                value={usageRetention}
                onChange={(e) => setUsageRetention(e.target.value)}
                placeholder={defaultText(defaultConfig.usage_retention_days)}
                invalid={Boolean(usageError)}
                aria-describedby={usageError ? usageErrorId : undefined}
              />
              {usageError ? (
                <p id={usageErrorId} role="alert" className="text-xs text-alert">
                  {usageError}
                </p>
              ) : config.usage_retention_days == null ? (
                <p className="text-xs text-ink-faint">
                  {defaultConfig.usage_retention_days != null
                    ? t("未设置（默认 {{val}} 天）", { val: defaultConfig.usage_retention_days })
                    : t("未设置")}
                </p>
              ) : (
                defaultConfig.usage_retention_days != null && (
                  <p className="text-xs text-ink-faint">
                    {t("默认值: {{val}} 天", { val: defaultConfig.usage_retention_days })}
                  </p>
                )
              )}
            </Fieldset>
          </div>
        </PanelBody>
      </Panel>

      {/* Purity Bands Section */}
      <Panel>
        <PanelHeader
          title={t("IP 纯净度等级分段")}
          description={t("配置网络节点 IP 纯净度评估的四个等级阈值（1 - 100 分）。")}
          meta={
            purityDirty ? (
              <SectionStateMark tone="warn" label={t("已修改")} />
            ) : (
              <SectionStateMark tone="neutral" label={t("系统默认")} />
            )
          }
        />
        <PanelBody className="space-y-6">
          <div className="grid grid-cols-1 gap-6 sm:grid-cols-2 lg:grid-cols-4">
            <Fieldset
              label={t("极优阈值 (Excellent)")}
              hint={t("纯净度分值大于等于该值评为极优。")}
              htmlFor={excellentFieldId}
            >
              <Input
                id={excellentFieldId}
                type="number"
                min="1"
                max="100"
                step="1"
                value={purityExcellent}
                onChange={(e) => setPurityExcellent(e.target.value)}
                placeholder={defaultText(defaultConfig.purity_bands?.excellent)}
                invalid={Boolean(purityError)}
                aria-describedby={purityError ? purityErrorId : undefined}
              />
              {config.purity_bands?.excellent == null && (
                <p className="text-xs text-ink-faint">
                  {defaultConfig.purity_bands?.excellent != null
                    ? t("未设置（默认 {{val}} 分）", { val: defaultConfig.purity_bands.excellent })
                    : t("未设置")}
                </p>
              )}
            </Fieldset>

            <Fieldset
              label={t("优良阈值 (Clean)")}
              hint={t("纯净度分值大于等于该值评为优良。")}
              htmlFor={cleanFieldId}
            >
              <Input
                id={cleanFieldId}
                type="number"
                min="1"
                max="100"
                step="1"
                value={purityClean}
                onChange={(e) => setPurityClean(e.target.value)}
                placeholder={defaultText(defaultConfig.purity_bands?.clean)}
                invalid={Boolean(purityError)}
                aria-describedby={purityError ? purityErrorId : undefined}
              />
              {config.purity_bands?.clean == null && (
                <p className="text-xs text-ink-faint">
                  {defaultConfig.purity_bands?.clean != null
                    ? t("未设置（默认 {{val}} 分）", { val: defaultConfig.purity_bands.clean })
                    : t("未设置")}
                </p>
              )}
            </Fieldset>

            <Fieldset
              label={t("一般阈值 (Fair)")}
              hint={t("纯净度分值大于等于该值评为一般。")}
              htmlFor={fairFieldId}
            >
              <Input
                id={fairFieldId}
                type="number"
                min="1"
                max="100"
                step="1"
                value={purityFair}
                onChange={(e) => setPurityFair(e.target.value)}
                placeholder={defaultText(defaultConfig.purity_bands?.fair)}
                invalid={Boolean(purityError)}
                aria-describedby={purityError ? purityErrorId : undefined}
              />
              {config.purity_bands?.fair == null && (
                <p className="text-xs text-ink-faint">
                  {defaultConfig.purity_bands?.fair != null
                    ? t("未设置（默认 {{val}} 分）", { val: defaultConfig.purity_bands.fair })
                    : t("未设置")}
                </p>
              )}
            </Fieldset>

            <Fieldset
              label={t("混合阈值 (Mixed)")}
              hint={t("纯净度分值大于等于该值评为混合等级。")}
              htmlFor={mixedFieldId}
            >
              <Input
                id={mixedFieldId}
                type="number"
                min="1"
                max="100"
                step="1"
                value={purityMixed}
                onChange={(e) => setPurityMixed(e.target.value)}
                placeholder={defaultText(defaultConfig.purity_bands?.mixed)}
                invalid={Boolean(purityError)}
                aria-describedby={purityError ? purityErrorId : undefined}
              />
              {config.purity_bands?.mixed == null && (
                <p className="text-xs text-ink-faint">
                  {defaultConfig.purity_bands?.mixed != null
                    ? t("未设置（默认 {{val}} 分）", { val: defaultConfig.purity_bands.mixed })
                    : t("未设置")}
                </p>
              )}
            </Fieldset>
          </div>
          {purityError && (
            <p id={purityErrorId} role="alert" className="text-xs text-alert">
              {purityError}
            </p>
          )}
        </PanelBody>
      </Panel>

      {/* Dialog: Confirm Reset to Default */}
      <DialogRoot open={resetModalOpen} onOpenChange={setResetModalOpen}>
        <DialogPortal>
          <DialogOverlay />
          <DialogContent className="w-full max-w-md p-6" aria-describedby={resetDescriptionId}>
            <DialogTitle className="text-base font-semibold text-ink">
              {t("确认恢复默认设置")}
            </DialogTitle>
            <p id={resetDescriptionId} className="mt-3 text-sm text-ink-soft">
              {t(
                "确定要将数据保留周期和纯净度等级阈值恢复为系统默认配置吗？未保存的手动更改将丢失。",
              )}
            </p>
            <div className="mt-6 flex items-center justify-end gap-3">
              <Button
                variant="secondary"
                onClick={() => setResetModalOpen(false)}
                disabled={updateMutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button
                variant="primary"
                onClick={handleResetToDefault}
                loading={updateMutation.isPending}
              >
                {t("确认恢复")}
              </Button>
            </div>
          </DialogContent>
        </DialogPortal>
      </DialogRoot>
    </div>
  );
}
