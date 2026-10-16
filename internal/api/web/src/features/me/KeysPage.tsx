import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Checkbox } from "../../components/ui/Checkbox";
import {
  DialogContent,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
} from "../../components/ui/Dialog";
import { Input } from "../../components/ui/Input";
import { PageHeader } from "../../components/ui/PageHeader";
import {
  Panel,
  PanelBody,
  PanelHeader,
  PanelToolbar,
} from "../../components/ui/Panel";
import {
  EmptyState,
  ErrorState,
  LoadingState,
} from "../../components/ui/QueryState";
import {
  Table,
  TableWrap,
  TBody,
  TD,
  TH,
  THead,
  TR,
} from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { Tooltip } from "../../components/ui/Tooltip";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { copyToClipboard } from "../../lib/clipboard";
import { formatApiErrorMessage } from "../../lib/error-message";
import { createKey, deleteKey, getKeys, getOverview } from "./api";
import type { AccessKeyInput, AccessKeyView, CreateKeyResponse } from "./types";

const NANOS_PER_MILLI = 1_000_000;

function formatNsDate(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(date);
}

function formatNsDateTime(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(date);
}

export function KeysPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();

  const [search, setSearch] = useState("");
  const [createDialogOpen, setCreateDialogOpen] = useState(false);
  const [createdSecret, setCreatedSecret] = useState<CreateKeyResponse | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<AccessKeyView | null>(null);

  const keysQuery = useQuery({
    queryKey: ["me-keys"],
    queryFn: getKeys,
  });

  const overviewQuery = useQuery({
    queryKey: ["me-overview"],
    queryFn: getOverview,
  });

  const overview = overviewQuery.data;
  const isRefreshing = keysQuery.isFetching || overviewQuery.isFetching;

  const handleRefresh = () => {
    void keysQuery.refetch();
    void overviewQuery.refetch();
  };

  const isLimitReached = Boolean(
    overview &&
      overview.max_keys > 0 &&
      overview.active_keys >= overview.max_keys,
  );

  const deleteMutation = useMutation({
    mutationFn: (keyID: string) => deleteKey(keyID),
    onSuccess: () => {
      showToast("success", t("密钥已删除"));
      setDeleteTarget(null);
      void queryClient.invalidateQueries({ queryKey: ["me-keys"] });
      void queryClient.invalidateQueries({ queryKey: ["me-overview"] });
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  const rawKeys = keysQuery.data?.items ?? [];
  const normalizedSearch = search.trim().toLowerCase();
  const filteredKeys = rawKeys.filter((key) => {
    if (!normalizedSearch) return true;
    return (
      key.name.toLowerCase().includes(normalizedSearch) ||
      key.credential_hint.toLowerCase().includes(normalizedSearch)
    );
  });

  const createButton = (
    <Button
      variant="primary"
      onClick={() => setCreateDialogOpen(true)}
      disabled={isLimitReached}
      title={
        isLimitReached
          ? t("已达到套餐密钥数量上限")
          : t("创建新的访问凭据密钥")
      }
    >
      <Plus size={14} />
      <span>{t("创建密钥")}</span>
    </Button>
  );

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("我的密钥")}
        description={t(
          "管理用于访问各平台代理服务的凭据密钥，支持平台授权与 IP 白名单控制。",
        )}
        actions={
          <div className="flex items-center gap-2">
            <Button
              variant="secondary"
              onClick={handleRefresh}
              disabled={isRefreshing}
              title={t("刷新")}
            >
              <RefreshCw
                size={14}
                className={isRefreshing ? "animate-spin" : undefined}
              />
              <span>{t("刷新")}</span>
            </Button>
            {isLimitReached ? (
              <Tooltip
                content={t(
                  "当前活跃密钥数已达到套餐上限（{{active}} / {{max}}），无法创建更多密钥。",
                  {
                    active: overview?.active_keys ?? 0,
                    max: overview?.max_keys ?? 0,
                  },
                )}
              >
                <span>{createButton}</span>
              </Tooltip>
            ) : (
              createButton
            )}
          </div>
        }
      />

      <Panel>
        <PanelHeader
          title={t("全部凭据密钥")}
          description={t(
            "每个密钥均包含独立平台访问策略与调用凭据，可随业务随时撤销。",
          )}
          meta={
            <div className="flex items-center gap-2">
              <Badge tone="neutral">
                {rawKeys.length}
                {overview && overview.max_keys > 0
                  ? ` / ${overview.max_keys}`
                  : ""}
              </Badge>
              {isLimitReached && (
                <Badge tone="warn">{t("配额已满")}</Badge>
              )}
            </div>
          }
        />

        <PanelToolbar className="flex flex-wrap items-center justify-between gap-3">
          <div className="relative max-w-sm min-w-[14rem] flex-1">
            <Search
              size={14}
              className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-ink-faint"
            />
            <Input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder={t("按名称或凭据前缀过滤...")}
              className="pl-8"
            />
          </div>
        </PanelToolbar>

        <PanelBody className="p-0">
          {keysQuery.isLoading ? (
            <LoadingState
              label={t("正在加载密钥列表...")}
              className="py-12"
            />
          ) : keysQuery.isError ? (
            <ErrorState
              message={formatApiErrorMessage(keysQuery.error, t)}
              onRetry={() => void keysQuery.refetch()}
              className="py-12"
            />
          ) : filteredKeys.length === 0 ? (
            <EmptyState
              title={t("暂无访问密钥")}
              hint={
                rawKeys.length === 0
                  ? t("点击右上角“创建密钥”生成首个代理凭据。")
                  : t("未找到匹配的密钥记录。")
              }
              className="py-12"
            />
          ) : (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>{t("密钥名称")}</TH>
                    <TH>{t("凭据前缀")}</TH>
                    <TH>{t("状态")}</TH>
                    <TH>{t("授权平台")}</TH>
                    <TH>{t("IP 白名单")}</TH>
                    <TH>{t("创建时间")}</TH>
                    <TH>{t("到期时间")}</TH>
                    <TH>{t("最后活跃")}</TH>
                    <TH className="text-right">{t("操作")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {filteredKeys.map((key) => (
                    <TR key={key.id}>
                      <TD className="font-medium text-ink">
                        {key.name || t("未命名密钥")}
                      </TD>
                      <TD className="readout font-mono text-xs text-ink-soft">
                        {key.credential_hint}
                      </TD>
                      <TD>
                        <Badge
                          tone={key.status === "active" ? "signal" : "warn"}
                        >
                          {key.status === "active"
                            ? t("正常")
                            : t("已禁用")}
                        </Badge>
                      </TD>
                      <TD className="text-xs">
                        {key.platforms && key.platforms.length > 0 ? (
                          <div className="flex flex-wrap gap-1">
                            {key.platforms.map((plat) => (
                              <Badge key={plat} tone="neutral">
                                {plat}
                              </Badge>
                            ))}
                          </div>
                        ) : (
                          <span className="text-ink-faint">
                            {t("全部平台")}
                          </span>
                        )}
                      </TD>
                      <TD className="text-xs">
                        {key.ip_allowlist && key.ip_allowlist.length > 0 ? (
                          <div className="max-w-xs truncate font-mono text-ink-soft" title={key.ip_allowlist.join(", ")}>
                            {key.ip_allowlist.join(", ")}
                          </div>
                        ) : (
                          <span className="text-ink-faint">
                            {t("不限制")}
                          </span>
                        )}
                      </TD>
                      <TD className="readout font-mono text-xs text-ink-soft">
                        {formatNsDate(key.created_at_ns)}
                      </TD>
                      <TD className="readout font-mono text-xs text-ink-soft">
                        {key.expires_at_ns > 0
                          ? formatNsDate(key.expires_at_ns)
                          : t("永不过期")}
                      </TD>
                      <TD className="readout font-mono text-xs text-ink-soft">
                        {key.last_used_ns > 0
                          ? formatNsDateTime(key.last_used_ns)
                          : t("从未")}
                      </TD>
                      <TD className="text-right">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setDeleteTarget(key)}
                          className="text-alert hover:text-alert"
                          title={t("删除此访问密钥")}
                        >
                          <Trash2 size={14} />
                          <span>{t("删除")}</span>
                        </Button>
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
          )}
        </PanelBody>
      </Panel>

      {/* Dialog: Create Key */}
      <CreateKeyDialog
        open={createDialogOpen}
        onOpenChange={setCreateDialogOpen}
        platforms={overview?.platforms ?? []}
        onSuccess={(created) => {
          setCreateDialogOpen(false);
          setCreatedSecret(created);
          showToast("success", t("密钥创建成功"));
          void queryClient.invalidateQueries({ queryKey: ["me-keys"] });
          void queryClient.invalidateQueries({ queryKey: ["me-overview"] });
        }}
      />

      {/* Dialog: Show Plaintext Secret (One-Time) */}
      <SecretOnceDialog
        data={createdSecret}
        onClose={() => setCreatedSecret(null)}
      />

      {/* Dialog: Delete Confirmation */}
      <DialogRoot
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
      >
        <DialogPortal>
          <DialogOverlay />
          <DialogContent className="w-full max-w-md p-6">
            <DialogTitle className="text-base font-semibold text-ink">
              {t("确认删除密钥")}
            </DialogTitle>
            <p className="mt-3 text-sm text-ink-soft">
              {t(
                "确定要删除密钥“{{name}}”吗？此操作不可撤销，删除后所有使用该密钥的客户端代理连接将立即被拒绝。",
                {
                  name:
                    deleteTarget?.name ||
                    deleteTarget?.credential_hint ||
                    "",
                },
              )}
            </p>
            <div className="mt-6 flex items-center justify-end gap-3">
              <Button
                variant="secondary"
                onClick={() => setDeleteTarget(null)}
                disabled={deleteMutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button
                variant="danger"
                onClick={() => {
                  if (deleteTarget) {
                    deleteMutation.mutate(deleteTarget.id);
                  }
                }}
                disabled={deleteMutation.isPending}
              >
                {deleteMutation.isPending
                  ? t("正在删除...")
                  : t("确认删除")}
              </Button>
            </div>
          </DialogContent>
        </DialogPortal>
      </DialogRoot>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Create Key Dialog
// ---------------------------------------------------------------------------

function CreateKeyDialog({
  open,
  onOpenChange,
  platforms,
  onSuccess,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  platforms: { platform_id: string; platform_name: string }[];
  onSuccess: (data: CreateKeyResponse) => void;
}) {
  const { t } = useI18n();
  const nameId = useId();
  const ipId = useId();
  const expiresId = useId();

  const [name, setName] = useState("");
  const [selectedPlatforms, setSelectedPlatforms] = useState<string[]>([]);
  const [ipRaw, setIpRaw] = useState("");
  const [expiresInDays, setExpiresInDays] = useState("");
  const [errorMsg, setErrorMsg] = useState("");

  const resetForm = () => {
    setName("");
    setSelectedPlatforms([]);
    setIpRaw("");
    setExpiresInDays("");
    setErrorMsg("");
  };

  const createMutation = useMutation({
    mutationFn: (input: AccessKeyInput) => createKey(input),
    onSuccess: (data) => {
      resetForm();
      onSuccess(data);
    },
    onError: (err) => {
      setErrorMsg(formatApiErrorMessage(err, t));
    },
  });

  const handlePlatformToggle = (platId: string) => {
    setSelectedPlatforms((prev) =>
      prev.includes(platId)
        ? prev.filter((p) => p !== platId)
        : [...prev, platId],
    );
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    setErrorMsg("");

    const trimmedName = name.trim();
    if (!trimmedName) {
      setErrorMsg(t("密钥名称不能为空"));
      return;
    }

    const ips = ipRaw
      .split(/[\n,]+/)
      .map((s) => s.trim())
      .filter(Boolean);

    const parsedDays = expiresInDays.trim()
      ? parseInt(expiresInDays.trim(), 10)
      : undefined;

    if (parsedDays !== undefined && (Number.isNaN(parsedDays) || parsedDays <= 0)) {
      setErrorMsg(t("有效天数必须为大于 0 的整数"));
      return;
    }

    const payload: AccessKeyInput = {
      name: trimmedName,
      platforms: selectedPlatforms.length > 0 ? selectedPlatforms : undefined,
      ip_allowlist: ips.length > 0 ? ips : undefined,
      expires_in_days: parsedDays,
    };

    createMutation.mutate(payload);
  };

  return (
    <DialogRoot
      open={open}
      onOpenChange={(isOpen) => {
        if (!isOpen) resetForm();
        onOpenChange(isOpen);
      }}
    >
      <DialogPortal>
        <DialogOverlay />
        <DialogContent className="w-full max-w-lg p-6">
          <DialogTitle className="text-base font-semibold text-ink">
            {t("创建访问凭据密钥")}
          </DialogTitle>

          <form onSubmit={handleSubmit} className="mt-4 space-y-4">
            {errorMsg && (
              <div className="rounded-control border border-alert/30 bg-alert-wash p-3 text-xs text-alert">
                {errorMsg}
              </div>
            )}

            <div className="space-y-1.5">
              <label
                htmlFor={nameId}
                className="block text-xs font-medium text-ink-soft"
              >
                {t("密钥名称")} *
              </label>
              <Input
                id={nameId}
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t("例如: 生产环境客户端")}
                required
              />
            </div>

            {/* Platform Selection */}
            <div className="space-y-1.5">
              <span className="block text-xs font-medium text-ink-soft">
                {t("授权平台（可选，不选则允许套餐下所有平台）")}
              </span>
              {platforms.length === 0 ? (
                <div className="rounded-control border border-glass-edge bg-glass p-2.5 text-xs text-ink-faint">
                  {t("当前套餐未限定特定平台，密钥默认可用所有平台。")}
                </div>
              ) : (
                <div className="grid grid-cols-2 gap-2 rounded-control border border-glass-edge bg-glass p-3">
                  {platforms.map((plat) => {
                    const isChecked = selectedPlatforms.includes(plat.platform_id);
                    return (
                      <label
                        key={plat.platform_id}
                        className="flex cursor-pointer items-center gap-2 text-xs text-ink hover:text-accent"
                      >
                        <Checkbox
                          checked={isChecked}
                          onChange={() => handlePlatformToggle(plat.platform_id)}
                        />
                        <span className="font-medium">
                          {plat.platform_name || plat.platform_id}
                        </span>
                        <span className="font-mono text-ink-faint">
                          ({plat.platform_id})
                        </span>
                      </label>
                    );
                  })}
                </div>
              )}
            </div>

            {/* IP Allowlist */}
            <div className="space-y-1.5">
              <label
                htmlFor={ipId}
                className="block text-xs font-medium text-ink-soft"
              >
                {t("IP 白名单 (可选，多个逗号或换行分隔)")}
              </label>
              <Input
                id={ipId}
                value={ipRaw}
                onChange={(e) => setIpRaw(e.target.value)}
                placeholder={t("留空不限制，或填: 1.2.3.4, 5.6.7.8/24")}
              />
            </div>

            {/* Expires In Days */}
            <div className="space-y-1.5">
              <label
                htmlFor={expiresId}
                className="block text-xs font-medium text-ink-soft"
              >
                {t("有效天数 (可选)")}
              </label>
              <Input
                id={expiresId}
                type="number"
                min="1"
                value={expiresInDays}
                onChange={(e) => setExpiresInDays(e.target.value)}
                placeholder={t("留空则永不过期")}
              />
            </div>

            <div className="mt-6 flex items-center justify-end gap-3">
              <Button
                type="button"
                variant="secondary"
                onClick={() => onOpenChange(false)}
                disabled={createMutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button
                type="submit"
                variant="primary"
                disabled={createMutation.isPending}
              >
                {createMutation.isPending
                  ? t("正在创建...")
                  : t("确认创建")}
              </Button>
            </div>
          </form>
        </DialogContent>
      </DialogPortal>
    </DialogRoot>
  );
}

// ---------------------------------------------------------------------------
// Secret Once Dialog (Single-view Plaintext Credential)
// ---------------------------------------------------------------------------

function SecretOnceDialog({
  data,
  onClose,
}: {
  data: CreateKeyResponse | null;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState("");

  const handleCopy = () => {
    if (!data?.credential) return;
    // A missing `navigator.clipboard` (plain-HTTP page) must not throw before the
    // operator is told the copy did not happen.
    void copyToClipboard(data.credential).then((ok) => {
      if (!ok) {
        setCopied(false);
        setCopyError(t("复制失败，请手动选择复制"));
        return;
      }
      setCopyError("");
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  };

  return (
    <DialogRoot
      open={Boolean(data)}
      onOpenChange={(open) => !open && onClose()}
    >
      <DialogPortal>
        <DialogOverlay />
        <DialogContent className="w-full max-w-lg p-6">
          <DialogTitle className="text-base font-semibold text-ink">
            {t("密钥凭据已生成")}
          </DialogTitle>

          <div className="mt-3 rounded-control border border-warn/30 bg-warn-wash p-3 text-xs text-warn">
            {t(
              "请立即复制并妥善保管此凭据！出于安全考虑，该密钥将不再向任何人展示。",
            )}
          </div>

          <div className="mt-4 space-y-1.5">
            <span className="micro block text-ink-faint">
              {t("明文访问凭据")}
            </span>
            <div className="flex items-center gap-2">
              <Input
                readOnly
                value={data?.credential || ""}
                className="font-mono text-xs"
              />
              <Button
                variant="secondary"
                onClick={handleCopy}
                className="shrink-0"
              >
                {copied ? (
                  <Check size={14} className="text-signal" />
                ) : (
                  <Copy size={14} />
                )}
                <span>{copied ? t("已复制") : t("复制")}</span>
              </Button>
            </div>
            {copyError ? <ErrorState message={copyError} className="mt-2" /> : null}
          </div>

          <div className="mt-6 flex items-center justify-end">
            <Button variant="primary" onClick={onClose}>
              {t("我已保存，关闭")}
            </Button>
          </div>
        </DialogContent>
      </DialogPortal>
    </DialogRoot>
  );
}
