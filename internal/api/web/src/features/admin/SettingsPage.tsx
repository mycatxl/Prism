import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, RefreshCw, RotateCcw, Save } from "lucide-react";
import { useId, useState } from "react";
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
import { Panel, PanelBody, PanelFooter, PanelHeader } from "../../components/ui/Panel";
import { ErrorState, LoadingState } from "../../components/ui/QueryState";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import {
  getDefaultSystemConfig,
  getSystemConfig,
  updateSystemConfig,
} from "./api";
import type { AdminSystemConfig, AdminSystemConfigPatch } from "./types";

export function SettingsPage() {
  const { t } = useI18n();

  const configQuery = useQuery({
    queryKey: ["admin-system-config"],
    queryFn: getSystemConfig,
  });

  const defaultQuery = useQuery({
    queryKey: ["admin-system-config-default"],
    queryFn: getDefaultSystemConfig,
  });

  if (configQuery.isLoading || defaultQuery.isLoading) {
    return (
      <div className="space-y-6">
        <PageHeader
          title={t("系统设置")}
          description={t("管理系统数据保留周期、IP 纯净度评估阈值与运行参数。")}
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
          title={t("系统设置")}
          description={t("管理系统数据保留周期、IP 纯净度评估阈值与运行参数。")}
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

  const config = configQuery.data ?? {
    audit_retention_days: 90,
    // The shipped window: 90 days for audit logs, 400 for usage (internal/config).
    usage_retention_days: 400,
    purity_bands: { excellent: 90, clean: 70, fair: 50, mixed: 30 },
  };

  /*
   * The form is keyed on the *content* of the config, not on when it was last
   * fetched. `dataUpdatedAt` changes on every background refetch — and the query
   * client refetches on window focus — so keying on it remounted this form and
   * threw away whatever the operator had typed without saving.
   */
  const configSignature = JSON.stringify(config);
  const defaultConfig = defaultQuery.data ?? config;

  return (
    <SettingsFormContent
      key={configSignature}
      config={config}
      defaultConfig={defaultConfig}
      onRefetch={() => {
        void configQuery.refetch();
        void defaultQuery.refetch();
      }}
    />
  );
}

function SettingsFormContent({
  config,
  defaultConfig,
  onRefetch,
}: {
  config: AdminSystemConfig;
  defaultConfig: AdminSystemConfig;
  onRefetch: () => void;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();

  const auditFieldId = useId();
  const usageFieldId = useId();
  const excellentFieldId = useId();
  const cleanFieldId = useId();
  const fairFieldId = useId();
  const mixedFieldId = useId();

  const [auditRetention, setAuditRetention] = useState(
    String(config.audit_retention_days ?? 90),
  );
  const [usageRetention, setUsageRetention] = useState(
    String(config.usage_retention_days ?? 400),
  );
  const [purityExcellent, setPurityExcellent] = useState(
    String(config.purity_bands?.excellent ?? 90),
  );
  const [purityClean, setPurityClean] = useState(
    String(config.purity_bands?.clean ?? 70),
  );
  const [purityFair, setPurityFair] = useState(
    String(config.purity_bands?.fair ?? 50),
  );
  const [purityMixed, setPurityMixed] = useState(
    String(config.purity_bands?.mixed ?? 30),
  );

  const [resetModalOpen, setResetModalOpen] = useState(false);
  const [errorMsg, setErrorMsg] = useState("");

  const updateMutation = useMutation({
    mutationFn: (patch: AdminSystemConfigPatch) => updateSystemConfig(patch),
    onSuccess: () => {
      showToast("success", t("系统设置已更新"));
      setErrorMsg("");
      void queryClient.invalidateQueries({ queryKey: ["admin-system-config"] });
    },
    onError: (err) => {
      const msg = formatApiErrorMessage(err, t);
      setErrorMsg(msg);
      showToast("error", msg);
    },
  });

  const handleSave = () => {
    setErrorMsg("");
    const patch: AdminSystemConfigPatch = {
      audit_retention_days: Number(auditRetention) || 0,
      usage_retention_days: Number(usageRetention) || 0,
      // Only the four keys the server decodes; it rejects unknown fields whole,
      // so an extra `poor` here failed every save with a 400.
      purity_bands: {
        excellent: Number(purityExcellent) || 0,
        clean: Number(purityClean) || 0,
        fair: Number(purityFair) || 0,
        mixed: Number(purityMixed) || 0,
      },
    };
    updateMutation.mutate(patch);
  };

  const handleResetToDefault = () => {
    const patch: AdminSystemConfigPatch = {
      audit_retention_days: defaultConfig.audit_retention_days,
      usage_retention_days: defaultConfig.usage_retention_days,
      purity_bands: defaultConfig.purity_bands,
    };
    updateMutation.mutate(patch, {
      onSuccess: () => {
        setResetModalOpen(false);
        showToast("success", t("已恢复为默认设置"));
      },
    });
  };

  const isAuditModified =
    defaultConfig.audit_retention_days != null &&
    Number(auditRetention) !== defaultConfig.audit_retention_days;

  const isUsageModified =
    defaultConfig.usage_retention_days != null &&
    Number(usageRetention) !== defaultConfig.usage_retention_days;

  const isPurityModified =
    defaultConfig.purity_bands != null &&
    (Number(purityExcellent) !== defaultConfig.purity_bands.excellent ||
      Number(purityClean) !== defaultConfig.purity_bands.clean ||
      Number(purityFair) !== defaultConfig.purity_bands.fair ||
      Number(purityMixed) !== defaultConfig.purity_bands.mixed);

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("系统设置")}
        description={t("管理系统数据保留周期、IP 纯净度评估阈值与运行参数。")}
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
              disabled={updateMutation.isPending}
            >
              {updateMutation.isPending ? (
                <span>{t("正在保存...")}</span>
              ) : (
                <>
                  <Save size={14} />
                  <span>{t("保存设置")}</span>
                </>
              )}
            </Button>
          </div>
        }
      />

      {errorMsg && (
        <div className="rounded-control border border-alert/30 bg-alert/10 px-4 py-3 text-xs text-alert">
          {errorMsg}
        </div>
      )}

      {/* Retention Section */}
      <Panel>
        <PanelHeader
          title={t("数据保留周期")}
          description={t("设置审计日志与用量统计数据的自动清理周期，0 或留空表示使用出厂窗口：审计 90 天、用量 400 天。")}
          meta={
            (isAuditModified || isUsageModified) ? (
              <Badge tone="accent">{t("已修改")}</Badge>
            ) : (
              <Badge tone="neutral">{t("系统默认")}</Badge>
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
              <div className="relative">
                <Input
                  id={auditFieldId}
                  type="number"
                  min="0"
                  step="1"
                  value={auditRetention}
                  onChange={(e) => setAuditRetention(e.target.value)}
                  placeholder="90"
                />
              </div>
              {defaultConfig.audit_retention_days != null && (
                <p className="mt-1 text-xs text-ink-faint">
                  {t("默认值: {{val}} 天", { val: defaultConfig.audit_retention_days })}
                </p>
              )}
            </Fieldset>

            <Fieldset
              label={t("用量记录保留天数")}
              hint={t("超过设定天数的每日用量统计数据将被自动清理。")}
              htmlFor={usageFieldId}
            >
              <div className="relative">
                <Input
                  id={usageFieldId}
                  type="number"
                  min="0"
                  step="1"
                  value={usageRetention}
                  onChange={(e) => setUsageRetention(e.target.value)}
                  placeholder="400"
                />
              </div>
              {defaultConfig.usage_retention_days != null && (
                <p className="mt-1 text-xs text-ink-faint">
                  {t("默认值: {{val}} 天", { val: defaultConfig.usage_retention_days })}
                </p>
              )}
            </Fieldset>
          </div>
        </PanelBody>
      </Panel>

      {/* Purity Bands Section */}
      <Panel>
        <PanelHeader
          title={t("IP 纯净度等级分段")}
          description={t("配置网络节点 IP 纯净度评估的四个等级阈值（0 - 100 分）。")}
          meta={
            isPurityModified ? (
              <Badge tone="accent">{t("已修改")}</Badge>
            ) : (
              <Badge tone="neutral">{t("系统默认")}</Badge>
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
                min="0"
                max="100"
                step="1"
                value={purityExcellent}
                onChange={(e) => setPurityExcellent(e.target.value)}
                placeholder="90"
              />
              {defaultConfig.purity_bands?.excellent != null && (
                <p className="mt-1 text-xs text-ink-faint">
                  {t("默认值: {{val}}", { val: defaultConfig.purity_bands.excellent })}
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
                min="0"
                max="100"
                step="1"
                value={purityClean}
                onChange={(e) => setPurityClean(e.target.value)}
                placeholder="70"
              />
              {defaultConfig.purity_bands?.clean != null && (
                <p className="mt-1 text-xs text-ink-faint">
                  {t("默认值: {{val}}", { val: defaultConfig.purity_bands.clean })}
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
                min="0"
                max="100"
                step="1"
                value={purityFair}
                onChange={(e) => setPurityFair(e.target.value)}
                placeholder="50"
              />
              {defaultConfig.purity_bands?.fair != null && (
                <p className="mt-1 text-xs text-ink-faint">
                  {t("默认值: {{val}}", { val: defaultConfig.purity_bands.fair })}
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
                min="0"
                max="100"
                step="1"
                value={purityMixed}
                onChange={(e) => setPurityMixed(e.target.value)}
                placeholder="30"
              />
              {defaultConfig.purity_bands?.mixed != null && (
                <p className="mt-1 text-xs text-ink-faint">
                  {t("默认值: {{val}}", { val: defaultConfig.purity_bands.mixed })}
                </p>
              )}
            </Fieldset>
          </div>
        </PanelBody>
      </Panel>

      {/* Registration Mode Section (Read-only as required) */}
      <Panel>
        <PanelHeader
          title={t("系统环境与注册模式")}
          description={t("由服务端启动参数与环境变量控制的静态运行时属性。")}
        />
        <PanelBody className="space-y-4">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div>
              <span className="micro block text-ink-faint">{t("注册模式")}</span>
              <div className="mt-1.5 flex items-center gap-2">
                <Badge tone="neutral">{t("只读")}</Badge>
                <span className="text-sm text-ink-soft">
                  {t("由环境变量 PRISM_REGISTRATION 控制")}
                </span>
              </div>
            </div>
            <div>
              <span className="micro block text-ink-faint">{t("配置生效模式")}</span>
              <div className="mt-1.5 flex items-center gap-2">
                <Badge tone="signal" dot>
                  {t("实时热更新")}
                </Badge>
                <span className="text-sm text-ink-soft">
                  {t("保存后立即在当前集群节点生效")}
                </span>
              </div>
            </div>
          </div>
        </PanelBody>
        <PanelFooter className="justify-end">
          <Button
            variant="primary"
            onClick={handleSave}
            disabled={updateMutation.isPending}
          >
            {updateMutation.isPending ? (
              <span>{t("正在保存...")}</span>
            ) : (
              <>
                <Check size={14} />
                <span>{t("保存修改")}</span>
              </>
            )}
          </Button>
        </PanelFooter>
      </Panel>

      {/* Dialog: Confirm Reset to Default */}
      <DialogRoot open={resetModalOpen} onOpenChange={setResetModalOpen}>
        <DialogPortal>
          <DialogOverlay />
          <DialogContent className="w-full max-w-md p-6">
            <DialogTitle className="text-base font-semibold text-ink">
              {t("确认恢复默认设置")}
            </DialogTitle>
            <p className="mt-3 text-sm text-ink-soft">
              {t("确定要将数据保留周期和纯净度等级阈值恢复为系统默认配置吗？未保存的手动更改将丢失。")}
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
                disabled={updateMutation.isPending}
              >
                {updateMutation.isPending ? t("正在保存...") : t("确认恢复")}
              </Button>
            </div>
          </DialogContent>
        </DialogPortal>
      </DialogRoot>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </div>
  );
}
