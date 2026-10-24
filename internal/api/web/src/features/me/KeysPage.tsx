import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Check, Copy, Plus, RefreshCw, Trash2 } from "lucide-react";
import { useId, useState } from "react";
import {
  CommandSurface,
  ConfirmDrawer,
  ControlButton,
  ControlCheckbox,
  ControlInput,
  ControlTextarea,
  DetailFact,
  EmptyWorkState,
  FilterChip,
  InspectorDrawer,
  ObjectList,
  ObjectRow,
  PageFrame,
  QueryBadge,
  StatusMark,
  WorkSurface,
} from "../../components/control-plane";
import { ToastContainer } from "../../components/ui/Toast";
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
  return new Intl.DateTimeFormat(locale, { year: "numeric", month: "2-digit", day: "2-digit" }).format(date);
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

function keyTone(status: string): "signal" | "warn" | "alert" | "neutral" {
  if (status === "active") return "signal";
  if (status === "disabled") return "warn";
  return "neutral";
}

function keyStatusLabel(status: string, t: (text: string) => string): string {
  if (status === "active") return t("正常");
  if (status === "disabled") return t("已禁用");
  return status;
}

export function KeysPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [search, setSearch] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [createdSecret, setCreatedSecret] = useState<CreateKeyResponse | null>(null);
  const [selected, setSelected] = useState<AccessKeyView | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<AccessKeyView | null>(null);

  const keysQuery = useQuery({ queryKey: ["me-keys"], queryFn: getKeys });
  const overviewQuery = useQuery({ queryKey: ["me-overview"], queryFn: getOverview });
  const overview = overviewQuery.data;
  const rawKeys = keysQuery.data?.items ?? [];
  const normalizedSearch = search.trim().toLowerCase();
  const filteredKeys = rawKeys.filter((key) => {
    if (!normalizedSearch) return true;
    return key.name.toLowerCase().includes(normalizedSearch) || key.credential_hint.toLowerCase().includes(normalizedSearch);
  });
  const limitReached = Boolean(overview && overview.max_keys > 0 && overview.active_keys >= overview.max_keys);

  const deleteMutation = useMutation({
    mutationFn: (keyID: string) => deleteKey(keyID),
    onSuccess: () => {
      showToast("success", t("密钥已删除"));
      setDeleteTarget(null);
      setSelected(null);
      void queryClient.invalidateQueries({ queryKey: ["me-keys"] });
      void queryClient.invalidateQueries({ queryKey: ["me-overview"] });
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const clearSearch = () => setSearch("");
  const refresh = () => {
    void keysQuery.refetch();
    void overviewQuery.refetch();
  };

  return (
    <PageFrame
      eyebrow="Self Service / access"
      title={t("我的密钥")}
      description={t("管理用于访问各平台代理服务的凭据密钥，支持平台授权与 IP 白名单控制。")}
      meta={<StatusMark tone={keysQuery.isError && !keysQuery.data ? "warn" : keysQuery.data ? "live" : "neutral"} label={keysQuery.isError && !keysQuery.data ? t("读取失败") : keysQuery.data ? t("数据已同步") : t("等待数据")} />}
      actions={
        <>
          <QueryBadge label={overview && overview.max_keys > 0 ? `${overview.active_keys} / ${overview.max_keys}` : `${rawKeys.length}`} tone={limitReached ? "warn" : "neutral"} />
          <ControlButton variant="secondary" size="sm" onPress={refresh} loading={keysQuery.isFetching || overviewQuery.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton>
          <ControlButton variant="primary" size="sm" onPress={() => setCreateOpen(true)} isDisabled={limitReached}><Plus size={14} aria-hidden />{t("创建密钥")}</ControlButton>
        </>
      }
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      <CommandSurface actions={normalizedSearch ? <ControlButton variant="ghost" size="sm" onPress={clearSearch}>{t("清除筛选")}</ControlButton> : <QueryBadge label={t("按名称或凭据前缀过滤")} />}>
        <ControlInput label={t("搜索密钥")} value={search} onChange={(event) => setSearch(event.target.value)} placeholder={t("按名称或凭据前缀过滤...")} />
      </CommandSurface>
      {normalizedSearch ? <div className="cp-active-filters"><FilterChip label={`${t("关键词")}: ${search.trim()}`} onRemove={clearSearch} /></div> : null}
      {limitReached ? <QueryBadge label={t("配额已满")} tone="warn" /> : null}

      <WorkSurface
        eyebrow="Credential registry"
        title={t("全部凭据密钥")}
        description={t("每个密钥均包含独立平台访问策略与调用凭据，可随业务随时撤销。")}
        meta={keysQuery.data ? t("共 {{count}} 个密钥", { count: rawKeys.length }) : t("未加载")}
      >
        {keysQuery.isError && !keysQuery.data ? (
          <EmptyWorkState title={t("读取失败")} detail={formatApiErrorMessage(keysQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void keysQuery.refetch()}>{t("重试")}</ControlButton>} />
        ) : null}
        {keysQuery.isLoading && !keysQuery.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载密钥列表...")}</div> : null}
        {!keysQuery.isLoading && !keysQuery.isError && filteredKeys.length === 0 ? <EmptyWorkState title={t("暂无访问密钥")} detail={rawKeys.length === 0 ? t("点击右上角“创建密钥”生成首个代理凭据。") : t("未找到匹配的密钥记录。")} action={rawKeys.length === 0 ? <ControlButton variant="primary" size="sm" onPress={() => setCreateOpen(true)}><Plus size={14} aria-hidden />{t("创建密钥")}</ControlButton> : undefined} /> : null}
        {filteredKeys.length > 0 ? (
          <ObjectList>
            {filteredKeys.map((key) => (
              <ObjectRow
                key={key.id}
                active={selected?.id === key.id}
                onClick={() => setSelected(key)}
                leading={<span aria-hidden>◈</span>}
                title={key.name || t("未命名密钥")}
                subtitle={`${key.credential_hint} · ${key.expires_at_ns > 0 ? t("到期 {{date}}", { date: formatNsDate(key.expires_at_ns) }) : t("永不过期")}`}
                facts={<><span>{key.platforms?.length ? key.platforms.join(", ") : t("全部平台")}</span><span>{key.ip_allowlist?.length ? t("{{count}} 个 IP", { count: key.ip_allowlist.length }) : t("不限制")}</span><span>{key.last_used_ns > 0 ? formatNsDateTime(key.last_used_ns) : t("从未")}</span></>}
                status={<StatusMark tone={keyTone(key.status)} label={keyStatusLabel(key.status, t)} />}
                action={<ArrowRight size={14} aria-hidden />}
              />
            ))}
          </ObjectList>
        ) : null}
      </WorkSurface>

      <InspectorDrawer
        open={Boolean(selected) && !createOpen}
        onOpenChange={(open) => { if (!open) setSelected(null); }}
        title={selected?.name || t("未命名密钥")}
        eyebrow="Credential inspector"
        footer={selected ? <><ControlButton variant="ghost" size="sm" onPress={() => setSelected(null)}>{t("关闭")}</ControlButton><ControlButton variant="danger" size="sm" onPress={() => setDeleteTarget(selected)}><Trash2 size={14} aria-hidden />{t("删除")}</ControlButton></> : undefined}
      >
        {selected ? (
          <div className="cp-stack">
            <StatusMark tone={keyTone(selected.status)} label={keyStatusLabel(selected.status, t)} />
            <dl className="cp-detail-facts">
              <DetailFact label={t("密钥名称")} value={selected.name || t("未命名密钥")} />
              <DetailFact label={t("凭据前缀")} value={selected.credential_hint} mono />
              <DetailFact label={t("状态")} value={keyStatusLabel(selected.status, t)} />
              <DetailFact label={t("授权平台")} value={selected.platforms?.length ? selected.platforms.join(", ") : t("全部平台")} />
              <DetailFact label={t("IP 白名单")} value={selected.ip_allowlist?.length ? selected.ip_allowlist.join(", ") : t("不限制")} mono />
              <DetailFact label={t("创建时间")} value={formatNsDateTime(selected.created_at_ns)} mono />
              <DetailFact label={t("到期时间")} value={selected.expires_at_ns > 0 ? formatNsDateTime(selected.expires_at_ns) : t("永不过期")} mono />
              <DetailFact label={t("最后活跃")} value={selected.last_used_ns > 0 ? formatNsDateTime(selected.last_used_ns) : t("从未")} mono />
            </dl>
            <QueryBadge label={t("明文访问凭据只在创建成功后显示一次")} tone="warn" />
          </div>
        ) : null}
      </InspectorDrawer>

      <CreateKeyDrawer
        open={createOpen}
        onOpenChange={setCreateOpen}
        platforms={overview?.platforms ?? []}
        onSuccess={(created) => {
          setCreateOpen(false);
          setCreatedSecret(created);
          showToast("success", t("密钥创建成功"));
          void queryClient.invalidateQueries({ queryKey: ["me-keys"] });
          void queryClient.invalidateQueries({ queryKey: ["me-overview"] });
        }}
      />
      <SecretOnceDrawer key={createdSecret?.key.id ?? "secret"} data={createdSecret} onClose={() => setCreatedSecret(null)} />
      <ConfirmDrawer
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => { if (!open) setDeleteTarget(null); }}
        title={t("确认删除密钥")}
        eyebrow={t("Destructive action")}
        message={t("确定要删除密钥“{{name}}”吗？此操作不可撤销，删除后所有使用该密钥的客户端代理连接将立即被拒绝。", { name: deleteTarget?.name || deleteTarget?.credential_hint || "" })}
        cancelLabel={t("取消")}
        confirmLabel={t("确认删除")}
        loading={deleteMutation.isPending}
        onConfirm={() => { if (deleteTarget) deleteMutation.mutate(deleteTarget.id); }}
      />
    </PageFrame>
  );
}

function CreateKeyDrawer({
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
    onError: (error) => setErrorMsg(formatApiErrorMessage(error, t)),
  });
  const togglePlatform = (platformID: string) => setSelectedPlatforms((current) => current.includes(platformID) ? current.filter((item) => item !== platformID) : [...current, platformID]);
  const handleSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    setErrorMsg("");
    const trimmedName = name.trim();
    if (!trimmedName) {
      setErrorMsg(t("密钥名称不能为空"));
      return;
    }
    const ips = ipRaw.split(/[\n,]+/).map((item) => item.trim()).filter(Boolean);
    const parsedDays = expiresInDays.trim() ? parseInt(expiresInDays.trim(), 10) : undefined;
    if (parsedDays !== undefined && (Number.isNaN(parsedDays) || parsedDays <= 0)) {
      setErrorMsg(t("有效天数必须为大于 0 的整数"));
      return;
    }
    createMutation.mutate({ name: trimmedName, platforms: selectedPlatforms.length ? selectedPlatforms : undefined, ip_allowlist: ips.length ? ips : undefined, expires_in_days: parsedDays });
  };

  return (
    <InspectorDrawer
      open={open}
      onOpenChange={(next) => { if (!next) { createMutation.reset(); resetForm(); } onOpenChange(next); }}
      title={t("创建访问凭据密钥")}
      eyebrow={t("Self Service")}
      footer={<><ControlButton variant="ghost" size="sm" onPress={() => onOpenChange(false)} isDisabled={createMutation.isPending}>{t("取消")}</ControlButton><ControlButton type="submit" form="create-key-form" variant="primary" size="sm" loading={createMutation.isPending}>{createMutation.isPending ? t("正在创建...") : t("确认创建")}</ControlButton></>}
    >
      <form id="create-key-form" className="cp-form-stack" onSubmit={handleSubmit}>
        {errorMsg ? <p role="alert" className="cp-form-error">{errorMsg}</p> : null}
        <ControlInput id={nameId} label={`${t("密钥名称")} *`} value={name} onChange={(event) => setName(event.target.value)} placeholder={t("例如: 生产环境客户端")} />
        <div className="cp-field">
          <span className="cp-field__label">{t("授权平台（可选，不选则允许套餐下所有平台）")}</span>
          {platforms.length === 0 ? <p className="cp-muted">{t("当前套餐未限定特定平台，密钥默认可用所有平台。")}</p> : <div className="grid gap-2 border border-rule bg-paper-inset p-3">{platforms.map((platform) => <ControlCheckbox key={platform.platform_id} isSelected={selectedPlatforms.includes(platform.platform_id)} onChange={() => togglePlatform(platform.platform_id)} label={platform.platform_name || platform.platform_id} hint={platform.platform_id} />)}</div>}
        </div>
        <ControlTextarea id={ipId} label={t("IP 白名单 (可选，多个逗号或换行分隔)")} value={ipRaw} onChange={(event) => setIpRaw(event.target.value)} placeholder={t("留空不限制，或填: 1.2.3.4, 5.6.7.8/24")} rows={4} />
        <ControlInput id={expiresId} label={t("有效天数 (可选)")} type="number" min="1" value={expiresInDays} onChange={(event) => setExpiresInDays(event.target.value)} placeholder={t("留空则永不过期")} />
      </form>
    </InspectorDrawer>
  );
}

function SecretOnceDrawer({ data, onClose }: { data: CreateKeyResponse | null; onClose: () => void }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState("");
  const handleCopy = () => {
    if (!data?.credential) return;
    void copyToClipboard(data.credential).then((ok) => {
      if (!ok) {
        setCopied(false);
        setCopyError(t("复制失败，请手动选择复制"));
        return;
      }
      setCopyError("");
      setCopied(true);
    });
  };
  return (
    <InspectorDrawer
      open={Boolean(data)}
      onOpenChange={(open) => { if (!open) onClose(); }}
      title={t("密钥凭据已生成")}
      eyebrow={t("Sensitive value")}
      footer={<ControlButton variant="primary" size="sm" onPress={onClose}>{t("我已保存，关闭")}</ControlButton>}
    >
      <div className="cp-stack">
        <StatusMark tone="warn" label={t("请立即复制并妥善保管此凭据！出于安全考虑，该密钥将不再向任何人展示。")} />
        <ControlInput label={t("明文访问凭据")} readOnly value={data?.credential || ""} className="font-mono" />
        <ControlButton variant="secondary" size="sm" onPress={handleCopy}><>{copied ? <Check size={14} aria-hidden /> : <Copy size={14} aria-hidden />}</>{copied ? t("已复制") : t("复制")}</ControlButton>
        {copyError ? <p role="alert" className="cp-form-error">{copyError}</p> : null}
        <QueryBadge label={t("关闭后将不再显示明文凭据")} tone="warn" />
      </div>
    </InspectorDrawer>
  );
}
