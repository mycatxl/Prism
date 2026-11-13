import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Copy, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import {
  ConfirmDrawer,
  ControlButton,
  ControlInput,
  ControlPagination,
  ControlSelect,
  DetailFact,
  EmptyWorkState,
  FilterChip,
  InspectorDrawer,
  ObjectList,
  ObjectRow,
  WorkspacePage,
  QueryBadge,
  StatusMark,
  WorkspaceCard,
} from "../../components/control-plane";
import { ToastContainer } from "../../components/control-plane";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { copyToClipboard } from "../../lib/clipboard";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatMicro, parseAmountToMicro } from "../../lib/money";
import { createRedeemCodes, deleteRedeemCode, listRedeemCodes } from "./api";
import type { AdminRedeemCode, AdminRedeemCodeItem } from "./types";

const DEFAULT_PAGE_SIZE = 20;
/** What the admin list endpoints accept; the server caps a single page at 100. */
const PAGE_SIZE_OPTIONS = [20, 50, 100];
const NANOS_PER_MILLI = 1_000_000;
type CodeState = "usable" | "disabled";
type CodeFilter = "all" | CodeState;

function formatNsTime(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return new Intl.DateTimeFormat(locale, { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(date);
}

function classifyCode(code: AdminRedeemCode): CodeState {
  return code.status === "active" ? "usable" : "disabled";
}

export function RedeemCodesPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE);
  const [search, setSearch] = useState("");
  const [stateFilter, setStateFilter] = useState<CodeFilter>("all");
  const [createOpen, setCreateOpen] = useState(false);
  const [selected, setSelected] = useState<AdminRedeemCode | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<AdminRedeemCode | null>(null);
  const [minted, setMinted] = useState<AdminRedeemCodeItem[]>([]);
  const [formPending, setFormPending] = useState(false);

  const codesQuery = useQuery({
    queryKey: ["admin-redeem-codes", pageSize, page * pageSize],
    queryFn: () => listRedeemCodes({ limit: pageSize, offset: page * pageSize }),
    placeholderData: (previous) => previous,
  });
  const codes = useMemo(() => codesQuery.data?.items ?? [], [codesQuery.data]);
  const total = codesQuery.data?.total ?? 0;
  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return codes.filter((code) => {
      if (stateFilter !== "all" && classifyCode(code) !== stateFilter) return false;
      if (!needle) return true;
      return [code.code_hint, code.note].join(" ").toLowerCase().includes(needle);
    });
  }, [codes, search, stateFilter]);
  const filtersActive = search.trim() !== "" || stateFilter !== "all";
  /* Server pagination: `filtered` only ever sees the rows this page loaded. */
  const loadedCount = codes.length;
  const matchedCount = filtered.length;
  const hasNextPage = codesQuery.data ? (page + 1) * pageSize < total : false;
  const clearFilters = () => { setSearch(""); setStateFilter("all"); setPage(0); };
  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["admin-redeem-codes"] });
  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteRedeemCode(id),
    onSuccess: () => {
      showToast("success", t("兑换码已作废"));
      setDeleteTarget(null);
      setSelected(null);
      void queryClient.invalidateQueries({ queryKey: ["admin-redeem-codes"] });
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const handleCopyAll = () => {
    void copyToClipboard(minted.map((code) => code.code).join("\n")).then((ok) => {
      if (!ok) {
        showToast("error", t("复制失败，请手动选择复制"));
        return;
      }
      showToast("success", t("兑换码已复制到剪贴板"));
    });
  };
  const statusTone = codesQuery.isError && !codesQuery.data ? "warn" : codesQuery.data ? "live" : "neutral";

  return (
    <WorkspacePage
      eyebrow="Customers / redemption registry"
      title={t("兑换码")}
      description={t("生成用于余额充值的兑换码。码只显示一次，请生成后立即分发。")}
      meta={<StatusMark tone={statusTone} label={codesQuery.isError && !codesQuery.data ? t("读取失败") : codesQuery.data ? t("目录已同步") : t("等待目录")} />}
      actions={<><ControlButton variant="secondary" size="sm" onPress={refresh} loading={codesQuery.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton><ControlButton variant="primary" size="sm" onPress={() => setCreateOpen(true)}><Plus size={14} aria-hidden />{t("生成兑换码")}</ControlButton></>}
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      {minted.length > 0 ? <WorkspaceCard
        eyebrow="One-time plaintext"
        title={t("新生成的兑换码")}
        description={t("这些码不会再显示第二次。关闭此工作面后将无法找回。")}
        meta={t("共 {{count}} 条", { count: minted.length })}
        actions={<><ControlButton variant="secondary" size="sm" onPress={handleCopyAll}><Copy size={14} aria-hidden />{t("全部复制")}</ControlButton><ControlButton variant="ghost" size="sm" onPress={() => setMinted([])}>{t("我已保存，关闭")}</ControlButton></>}
      >
        <ObjectList>
          {minted.map((code) => <ObjectRow key={code.id} leading={<span className="workspace-platform-row__mark" aria-hidden>$</span>} title={code.code} subtitle={t("仅在本次生成结果中可见")} facts={<span className="readout">{formatMicro(code.amount_micro)}<small>USDT</small></span>} status={<QueryBadge tone="warn" label={t("一次性") } />} />)}
        </ObjectList>
      </WorkspaceCard> : null}

      <section className="workspace-command">
        <div className="workspace-command__body">
          <ControlInput label={t("搜索兑换码")} hint={t("仅匹配当前页已加载的兑换码")} value={search} onChange={(event) => { setSearch(event.target.value); setPage(0); }} placeholder={t("按末四位提示或备注搜索")} />
          <ControlSelect
            hint={t("状态筛选仅作用于当前页。")}
            label={t("状态")}
            items={[
              { id: "all", label: t("全部状态") },
              { id: "usable", label: t("可用") },
              { id: "disabled", label: t("已失效") },
            ]}
            selectedKey={stateFilter}
            onSelectionChange={(key) => {
              if (key === "all" || key === "usable" || key === "disabled") {
                setStateFilter(key);
                setPage(0);
              }
            }}
          />
        </div>
        <div className="workspace-command__actions"><Search size={14} aria-hidden />{filtersActive ? <ControlButton variant="ghost" size="sm" onPress={clearFilters}>{t("清除筛选")}</ControlButton> : null}</div>
      </section>
      {filtersActive ? <div className="workspace-active-filters" aria-label={t("当前筛选")}>
        {search.trim() ? <FilterChip label={`${t("关键词")}: ${search.trim()}`} onRemove={() => { setSearch(""); setPage(0); }} /> : null}
        {stateFilter !== "all" ? <FilterChip label={`${t("状态")}: ${stateFilter === "usable" ? t("可用") : t("已失效")}`} onRemove={() => { setStateFilter("all"); setPage(0); }} /> : null}
      </div> : null}

      <WorkspaceCard headerless eyebrow="Customer objects" title={t("兑换码列表")} meta={codesQuery.data ? (filtersActive ? t("本页匹配 {{matched}} / 已加载 {{loaded}} 条 · 服务端共 {{total}} 条", { matched: matchedCount, loaded: loadedCount, total }) : t("共 {{count}} 条", { count: total })) : t("未加载")} description={t("已生成的兑换码只显示末四位，用于辨认与作废。") }>
        {codesQuery.isError && !codesQuery.data ? <EmptyWorkState embedded title={t("兑换码目录读取失败")} detail={formatApiErrorMessage(codesQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void codesQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
        {codesQuery.isLoading && !codesQuery.data ? <div className="workspace-register-state"><span className="workspace-spinner" aria-hidden />{t("正在加载兑换码")}</div> : null}
        {!codesQuery.isLoading && !codesQuery.isError && codes.length === 0 ? <EmptyWorkState embedded title={t("暂无兑换码")} detail={t("生成兑换码后，可在这里查看使用情况与作废。") } action={<ControlButton variant="primary" size="sm" onPress={() => setCreateOpen(true)}><Plus size={14} aria-hidden />{t("生成兑换码")}</ControlButton>} /> : null}
        {!codesQuery.isLoading && !codesQuery.isError && codes.length > 0 && filtered.length === 0 ? <EmptyWorkState embedded title={t("当前页没有匹配的兑换码")} detail={t("搜索与状态筛选只作用于当前页；可翻到下一页继续查找，或清除筛选。") } action={<>{hasNextPage ? <ControlButton variant="secondary" size="sm" onPress={() => setPage(page + 1)}>{t("下一页")}</ControlButton> : null}<ControlButton variant="secondary" size="sm" onPress={clearFilters}>{t("清除筛选")}</ControlButton></>} /> : null}
        {filtered.length > 0 ? <ObjectList>
          {filtered.map((code) => <ObjectRow
            key={code.id}
            active={selected?.id === code.id}
            onClick={() => setSelected(code)}
            leading={<span className="workspace-platform-row__mark" aria-hidden>$</span>}
            title={`****${code.code_hint}`}
            subtitle={<span className="readout">{formatMicro(code.amount_micro)}<small>USDT</small></span>}
            facts={<><span>{t("使用")} {code.used_count} / {code.max_uses > 0 ? code.max_uses : t("不限")}</span><span>{t("到期")} {code.expires_at_ns > 0 ? formatNsTime(code.expires_at_ns) : t("永不过期")}</span><span>{t("创建")} {formatNsTime(code.created_at_ns)}</span></>}
            status={<StatusMark tone={classifyCode(code) === "usable" ? "signal" : "alert"} label={classifyCode(code) === "usable" ? t("可用") : t("已失效")} />}
            action={<ArrowRight size={14} aria-hidden />}
          />)}
        </ObjectList> : null}
        {codesQuery.data ? <ControlPagination
          page={page}
          totalItems={total}
          pageSize={pageSize}
          pageSizes={PAGE_SIZE_OPTIONS}
          disabled={codesQuery.isFetching}
          labels={{ summary: ({ page: current, pages, start, end, total: count }) => t("第 {{page}} / {{pages}} 页 · {{start}}-{{end}} / {{total}}", { page: current, pages, start, end, total: count }), perPage: t("每页"), pageOption: (size) => `${size} / ${t("页")}`, previous: t("上一页"), next: t("下一页") }}
          onPageChange={setPage}
          onPageSizeChange={(next) => { setPageSize(next); setPage(0); }}
        /> : null}
      </WorkspaceCard>

      <InspectorDrawer
        open={Boolean(selected) && !createOpen}
        onOpenChange={(open) => { if (!open) setSelected(null); }}
        title={selected ? `****${selected.code_hint}` : t("兑换码")}
        eyebrow={t("Customer / redemption inspector")}
        footer={selected ? <><ControlButton variant="ghost" size="sm" onPress={() => setSelected(null)}>{t("关闭")}</ControlButton><ControlButton variant="danger" size="sm" onPress={() => setDeleteTarget(selected)}><Trash2 size={14} aria-hidden />{t("作废")}</ControlButton></> : undefined}
      >
        {selected ? <div className="workspace-stack">
          <div className="workspace-platform-inspector__identity"><span className="workspace-platform-row__mark" aria-hidden>$</span><div><strong className="mono">****{selected.code_hint}</strong><code>{selected.id}</code></div><StatusMark tone={classifyCode(selected) === "usable" ? "signal" : "alert"} label={classifyCode(selected) === "usable" ? t("可用") : t("已失效")} /></div>
          <dl className="workspace-detail-facts"><DetailFact label={t("面额")} value={<span className="readout">{formatMicro(selected.amount_micro)}<small>USDT</small></span>} mono /><DetailFact label={t("使用次数")} value={`${selected.used_count} / ${selected.max_uses > 0 ? selected.max_uses : t("不限")}`} mono /><DetailFact label={t("过期时间")} value={selected.expires_at_ns > 0 ? formatNsTime(selected.expires_at_ns) : t("永不过期")} mono /><DetailFact label={t("创建时间")} value={formatNsTime(selected.created_at_ns)} mono /><DetailFact label={t("备注")} value={selected.note || "-"} /></dl>
          <QueryBadge label={t("兑换码明文不会在列表或详情中恢复") } />
        </div> : null}
      </InspectorDrawer>

      <InspectorDrawer
        open={createOpen}
        onOpenChange={(open) => { if (!open && !formPending) setCreateOpen(false); }}
        title={t("生成兑换码")}
        eyebrow={t("Administrator action")}
        footer={<div className="workspace-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => setCreateOpen(false)} isDisabled={formPending}>{t("取消")}</ControlButton><ControlButton type="submit" form="redeem-form" variant="primary" size="sm" isDisabled={formPending} loading={formPending}>{t("生成")}</ControlButton></div>}
      >
        {createOpen ? <CreateRedeemContent onPendingChange={setFormPending} onSuccess={(batch) => { setMinted(batch); setCreateOpen(false); showToast("success", t("兑换码已生成，请立即复制保存")); void queryClient.invalidateQueries({ queryKey: ["admin-redeem-codes"] }); }} /> : null}
      </InspectorDrawer>

      <ConfirmDrawer
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => { if (!open) setDeleteTarget(null); }}
        title={t("作废兑换码")}
        eyebrow={t("Destructive action")}
        message={<>{t("作废后该码无法再被兑换。已充值的余额不受影响。")} {deleteTarget ? <strong className="mono">****{deleteTarget.code_hint}</strong> : null}</>}
        cancelLabel={t("取消")}
        confirmLabel={t("确认作废")}
        loading={deleteMutation.isPending}
        onConfirm={() => { if (deleteTarget) deleteMutation.mutate(deleteTarget.id); }}
      />
    </WorkspacePage>
  );
}

function CreateRedeemContent({ onPendingChange, onSuccess }: { onPendingChange: (pending: boolean) => void; onSuccess: (batch: AdminRedeemCodeItem[]) => void }) {
  const { t } = useI18n();
  const { toasts, showToast, dismissToast } = useToast();
  const [amount, setAmount] = useState("10");
  const [count, setCount] = useState("1");
  const [maxUses, setMaxUses] = useState("1");
  const [expiresInDays, setExpiresInDays] = useState("0");
  const [note, setNote] = useState("");
  const createMutation = useMutation({
    mutationFn: () => {
      const amountMicro = parseAmountToMicro(amount);
      if (amountMicro === null || amountMicro <= 0) throw new Error(t("请输入有效金额，最多六位小数。"));
      const parsedCount = Number(count);
      if (!Number.isInteger(parsedCount) || parsedCount < 1) throw new Error(t("生成数量必须是不小于 1 的整数。"));
      const parsedUses = Number(maxUses);
      if (!Number.isInteger(parsedUses) || parsedUses < 0) throw new Error(t("可用次数必须是不小于 0 的整数。"));
      const parsedDays = Number(expiresInDays);
      if (!Number.isInteger(parsedDays) || parsedDays < 0) throw new Error(t("有效期必须是不小于 0 的整数。"));
      return createRedeemCodes({ amount_micro: amountMicro, max_uses: parsedUses, expires_in_days: parsedDays, note, count: parsedCount });
    },
    onMutate: () => onPendingChange(true),
    onSuccess: (batch) => onSuccess(batch.codes),
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
    onSettled: () => onPendingChange(false),
  });
  return <>
    <form id="redeem-form" className="workspace-form-stack" onSubmit={(event) => { event.preventDefault(); createMutation.mutate(); }}>
      <ControlInput label={t("面额 (USDT)")} hint={t("每个码可充值的金额，最多六位小数。")} value={amount} onChange={(event) => setAmount(event.target.value)} inputMode="decimal" className="font-mono" />
      <ControlInput label={t("生成数量")} hint={t("一次最多生成 100 个。") } value={count} onChange={(event) => setCount(event.target.value)} inputMode="numeric" className="font-mono" />
      <div className="workspace-form-grid"><ControlInput label={t("可用次数")} hint={t("0 表示不限次数。") } value={maxUses} onChange={(event) => setMaxUses(event.target.value)} inputMode="numeric" className="font-mono" /><ControlInput label={t("有效期 (天)")} hint={t("0 表示永不过期。") } value={expiresInDays} onChange={(event) => setExpiresInDays(event.target.value)} inputMode="numeric" className="font-mono" /></div>
      <ControlInput label={t("备注")} value={note} onChange={(event) => setNote(event.target.value)} placeholder={t("例如：社区活动第一批")} />
      {createMutation.isError ? <p className="workspace-form-error">{formatApiErrorMessage(createMutation.error, t)}</p> : null}
    </form>
    <ToastContainer toasts={toasts} onDismiss={dismissToast} />
  </>;
}
