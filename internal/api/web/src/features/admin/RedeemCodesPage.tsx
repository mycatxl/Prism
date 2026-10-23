import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { Button } from "../../components/ui/Button";
import {
  DialogContent,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
} from "../../components/ui/Dialog";
import { Fieldset, Input } from "../../components/ui/Input";
import { Page, PageHeader, PageMeta } from "../../components/ui/PageHeader";
import {
  Panel,
  PanelBody,
  PanelFooter,
  PanelHeader,
  PanelToolbar,
} from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Select } from "../../components/ui/Select";
import {
  Table,
  TableWrap,
  TBody,
  TD,
  TDClip,
  TDNum,
  TH,
  THead,
  TR,
} from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { copyToClipboard } from "../../lib/clipboard";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatMicro, parseAmountToMicro } from "../../lib/money";
import { createRedeemCodes, deleteRedeemCode, listRedeemCodes } from "./api";
import type { AdminRedeemCode, AdminRedeemCodeItem } from "./types";

/*
 * RedeemCodesPage mints and withdraws top-up codes, on the Signal Console
 * rhythm: the page header states the page and carries the mint action, the
 * minted batch gets its own panel until it is copied, and one panel carries a
 * toolbar of search + state + refresh over the code list. Amounts are readouts
 * with their unit set small; the state column is the dot-and-word pair.
 *
 * The plaintext of a code exists exactly once: in the response that created it.
 * Everything after that shows only the hint, because the database stores an HMAC
 * and there is nothing to show. That is why minting opens a panel with the codes
 * and tells the operator to copy them now.
 *
 * The list endpoint takes no filters, so search and state narrow the page the
 * cache holds; the limit/offset paging stays server-side exactly as before.
 */

const PAGE_SIZE = 20;
const NANOS_PER_MILLI = 1_000_000;

function formatNsTime(ns?: number | null): string {
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
    hour12: false,
  }).format(date);
}

/**
 * The state a code is in, as the redeem path will treat it: the server reports
 * an exhausted or expired code as `disabled`, and the list keeps that single
 * split — 可用, or 已失效.
 */
type CodeState = "usable" | "disabled";
type CodeFilter = "all" | CodeState;

function classifyCode(code: AdminRedeemCode): CodeState {
  return code.status === "active" ? "usable" : "disabled";
}

/** The `.state-dot` variant for each code state; `usable` is the live one. */
const CODE_DOT: Record<CodeState, string> = {
  usable: "state-dot state-dot--signal",
  disabled: "state-dot",
};

export function RedeemCodesPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();

  const [offset, setOffset] = useState(0);
  const [search, setSearch] = useState("");
  const [stateFilter, setStateFilter] = useState<CodeFilter>("all");
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<AdminRedeemCode | null>(null);
  const [minted, setMinted] = useState<AdminRedeemCodeItem[]>([]);

  const {
    data: codesData,
    isLoading,
    isError,
    error,
    isFetching,
  } = useQuery({
    queryKey: ["admin-redeem-codes", offset],
    queryFn: () => listRedeemCodes({ limit: PAGE_SIZE, offset }),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteRedeemCode(id),
    onSuccess: () => {
      showToast("success", t("兑换码已作废"));
      setDeleteTarget(null);
      void queryClient.invalidateQueries({ queryKey: ["admin-redeem-codes"] });
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });
  const codes = useMemo(() => codesData?.items ?? [], [codesData]);
  const total = codesData?.total ?? 0;
  const currentPage = Math.floor(offset / PAGE_SIZE) + 1;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  /*
   * Search and state narrow the rows this page already holds: the needle
   * matches the hint and the note — the two things that tell a minted code
   * apart once its plaintext is gone.
   */
  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return codes.filter((code) => {
      if (stateFilter !== "all" && classifyCode(code) !== stateFilter) return false;
      if (!needle) return true;
      const haystack = [code.code_hint, code.note].join(" ").toLowerCase();
      return haystack.includes(needle);
    });
  }, [codes, search, stateFilter]);

  const filtersActive = search.trim() !== "" || stateFilter !== "all";
  const clearFilters = () => {
    setSearch("");
    setStateFilter("all");
  };

  // The page's own cache entry: the list this panel reads.
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["admin-redeem-codes"] });
  };

  const handleCopyAll = () => {
    const text = minted.map((code) => code.code).join("\n");
    void copyToClipboard(text).then((ok) => {
      if (!ok) {
        showToast("error", t("复制失败，请手动选择复制"));
        return;
      }
      showToast("success", t("兑换码已复制到剪贴板"));
    });
  };

  return (
    <Page bleed>
      <PageHeader
        title={t("兑换码")}
        description={t("生成用于余额充值的兑换码。码只显示一次，请生成后立即分发。")}
        meta={
          <PageMeta
            label={t("兑换码总数")}
            value={codesData ? total.toLocaleString() : "--"}
          />
        }
        actions={
          <Button variant="primary" onClick={() => setCreateOpen(true)}>
            <Plus size={16} />
            <span>{t("生成兑换码")}</span>
          </Button>
        }
      />

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <div className="page-content page-content--fill space-y-4">
        {minted.length > 0 && (
          <Panel>
            <PanelHeader
              title={t("新生成的兑换码")}
              description={t("这些码不会再显示第二次。关闭此面板后将无法找回。")}
              meta={t("共 {{count}} 条", { count: minted.length })}
              actions={
                <Button variant="secondary" size="sm" onClick={handleCopyAll}>
                  <Copy size={14} />
                  <span>{t("全部复制")}</span>
                </Button>
              }
            />
            <PanelBody className="space-y-1.5">
              {minted.map((code) => (
                <div
                  key={code.id}
                  className="flex items-center justify-between gap-3 rounded-control border border-rule bg-paper-inset px-3 py-2"
                >
                  <span className="mono truncate text-sm tracking-wider text-ink">
                    {code.code}
                  </span>
                  <span className="readout shrink-0 text-xs text-ink-soft">
                    {formatMicro(code.amount_micro)}
                    <small>USDT</small>
                  </span>
                </div>
              ))}
            </PanelBody>
            <PanelFooter>
              <Button variant="secondary" onClick={() => setMinted([])}>
                {t("我已保存，关闭")}
              </Button>
            </PanelFooter>
          </Panel>
        )}

        <Panel className="flex min-w-0 flex-col">
          <PanelHeader
            title={t("兑换码列表")}
            description={t("已生成的兑换码只显示末四位，用于辨认与作废。")}
            meta={codesData ? t("共 {{count}} 条", { count: total }) : "--"}
          />

          <PanelToolbar>
            <label htmlFor="redeem-codes-search" className="relative block min-w-48 sm:w-64">
              <Search
                size={13}
                aria-hidden
                className="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-ink-faint"
              />
              <Input
                id="redeem-codes-search"
                className="pl-7 text-xs"
                placeholder={t("搜索兑换码")}
                aria-label={t("搜索兑换码")}
                value={search}
                onChange={(event) => {
                  setSearch(event.target.value);
                  setOffset(0);
                }}
              />
            </label>
            <Select
              className="w-32"
              aria-label={t("状态")}
              value={stateFilter}
              onChange={(event) => {
                setStateFilter(event.target.value as CodeFilter);
                setOffset(0);
              }}
            >
              <option value="all">{t("全部状态")}</option>
              <option value="usable">{t("可用")}</option>
              <option value="disabled">{t("已失效")}</option>
            </Select>
            {filtersActive && (
              <Button variant="ghost" size="sm" onClick={clearFilters}>
                {t("清除筛选")}
              </Button>
            )}
            <div className="ml-auto flex items-center gap-1.5">
              <Button
                variant="secondary"
                size="sm"
                onClick={refresh}
                disabled={isFetching}
                title={t("刷新")}
              >
                <RefreshCw
                  size={15}
                  className={isFetching ? "animate-spin" : undefined}
                />
                <span>{t("刷新")}</span>
              </Button>
            </div>
          </PanelToolbar>

          {isLoading ? (
            <PanelBody>
              <LoadingState label={t("正在加载兑换码...")} />
            </PanelBody>
          ) : isError ? (
            <PanelBody>
              <ErrorState message={formatApiErrorMessage(error, t)} onRetry={refresh} />
            </PanelBody>
          ) : codes.length === 0 ? (
            <PanelBody>
              <EmptyState
                title={t("暂无兑换码")}
                hint={t("生成兑换码后，可在这里查看使用情况与作废。")}
                action={
                  <Button variant="primary" onClick={() => setCreateOpen(true)}>
                    <Plus size={14} />
                    <span>{t("生成兑换码")}</span>
                  </Button>
                }
              />
            </PanelBody>
          ) : filtered.length === 0 ? (
            <PanelBody>
              <EmptyState
                title={t("没有匹配的兑换码")}
                hint={t("调整搜索或状态筛选，或清除筛选查看全部。")}
                action={
                  <Button variant="secondary" onClick={clearFilters}>
                    {t("清除筛选")}
                  </Button>
                }
              />
            </PanelBody>
          ) : (
            <TableWrap aria-busy={isFetching}>
              <Table className="min-w-[960px]">
                <THead>
                  <TR>
                    <TH>{t("末四位")}</TH>
                    <TH className="text-right">{t("面额")}</TH>
                    <TH className="text-right">{t("已用 / 次数")}</TH>
                    <TH>{t("状态")}</TH>
                    <TH>{t("过期时间")}</TH>
                    <TH>{t("备注")}</TH>
                    <TH>{t("创建时间")}</TH>
                    <TH className="text-right">{t("操作")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {filtered.map((code) => {
                    const state = classifyCode(code);
                    return (
                      <TR key={code.id}>
                        <TD>
                          <span className="mono text-sm tracking-wider text-ink">
                            ****{code.code_hint}
                          </span>
                        </TD>
                        <TDNum className="whitespace-nowrap">
                          <span className="readout">
                            {formatMicro(code.amount_micro)}
                            <small>USDT</small>
                          </span>
                        </TDNum>
                        <TDNum className="whitespace-nowrap">
                          <span className="readout">
                            {code.used_count}
                            <small>
                              {" "}
                              / {code.max_uses > 0 ? code.max_uses : t("无限制")}
                            </small>
                          </span>
                        </TDNum>
                        <TD className="whitespace-nowrap">
                          <span className="inline-flex items-center gap-2">
                            <span aria-hidden className={CODE_DOT[state]} />
                            <span className="state-text">
                              {state === "usable" ? t("可用") : t("已失效")}
                            </span>
                          </span>
                        </TD>
                        <TD className="whitespace-nowrap text-xs text-ink-soft">
                          {code.expires_at_ns > 0
                            ? formatNsTime(code.expires_at_ns)
                            : t("永不过期")}
                        </TD>
                        <TDClip className="text-xs text-ink-faint">
                          {code.note || "-"}
                        </TDClip>
                        <TD className="whitespace-nowrap readout text-xs text-ink-soft">
                          {formatNsTime(code.created_at_ns)}
                        </TD>
                        <TD className="text-right">
                          <Button
                            variant="ghost"
                            size="icon"
                            className="text-ink-faint hover:bg-alert-wash hover:text-alert"
                            onClick={() => setDeleteTarget(code)}
                            title={t("作废此兑换码")}
                            aria-label={t("作废此兑换码")}
                          >
                            <Trash2 size={14} />
                          </Button>
                        </TD>
                      </TR>
                    );
                  })}
                </TBody>
              </Table>
            </TableWrap>
          )}

          <PanelFooter>
            <p className="text-xs text-ink-soft">
              {filtersActive
                ? t("第 {{current}} / {{total}} 页 · 本页匹配 {{shown}} 条", {
                    current: currentPage,
                    total: totalPages,
                    shown: filtered.length,
                  })
                : t("第 {{current}} / {{total}} 页 (共 {{count}} 条)", {
                    current: currentPage,
                    total: totalPages,
                    count: total,
                  })}
            </p>
            <div className="flex items-center gap-2">
              <Button
                variant="secondary"
                disabled={offset === 0}
                onClick={() => setOffset((prev) => Math.max(0, prev - PAGE_SIZE))}
              >
                {t("上一页")}
              </Button>
              <Button
                variant="secondary"
                disabled={offset + PAGE_SIZE >= total}
                onClick={() => setOffset((prev) => prev + PAGE_SIZE)}
              >
                {t("下一页")}
              </Button>
            </div>
          </PanelFooter>
        </Panel>
      </div>

      <CreateRedeemDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onSuccess={(batch) => {
          setMinted(batch);
          setCreateOpen(false);
          showToast("success", t("兑换码已生成，请立即复制保存"));
          void queryClient.invalidateQueries({ queryKey: ["admin-redeem-codes"] });
        }}
      />

      <DialogRoot
        open={deleteTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteTarget(null);
        }}
      >
        <DialogPortal>
          <DialogOverlay />
          <DialogContent className="w-full max-w-md">
            <div className="border-b border-rule px-5 py-4">
              <DialogTitle>{t("作废兑换码")}</DialogTitle>
            </div>
            <div className="space-y-3 px-5 py-4 text-sm leading-relaxed text-ink-soft">
              <p>{t("作废后该码无法再被兑换。已充值的余额不受影响。")}</p>
              {deleteTarget && (
                <p className="mono text-sm tracking-wider text-ink">
                  ****{deleteTarget.code_hint}
                  <span className="readout ml-2 text-xs text-ink-soft">
                    {formatMicro(deleteTarget.amount_micro)}
                    <small>USDT</small>
                  </span>
                </p>
              )}
            </div>
            <div className="flex items-center justify-end gap-2 border-t border-rule bg-rail px-5 py-3">
              <Button variant="secondary" onClick={() => setDeleteTarget(null)}>
                {t("取消")}
              </Button>
              <Button
                variant="danger"
                loading={deleteMutation.isPending}
                onClick={() => {
                  if (deleteTarget) deleteMutation.mutate(deleteTarget.id);
                }}
              >
                {t("确认作废")}
              </Button>
            </div>
          </DialogContent>
        </DialogPortal>
      </DialogRoot>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </Page>
  );
}

function CreateRedeemContent({
  onOpenChange,
  onSuccess,
}: {
  onOpenChange: (open: boolean) => void;
  onSuccess: (batch: AdminRedeemCodeItem[]) => void;
}) {
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
      if (amountMicro === null || amountMicro <= 0) {
        throw new Error(t("请输入有效金额，最多六位小数。"));
      }
      const parsedCount = Number(count);
      if (!Number.isInteger(parsedCount) || parsedCount < 1) {
        throw new Error(t("生成数量必须是不小于 1 的整数。"));
      }
      const parsedUses = Number(maxUses);
      if (!Number.isInteger(parsedUses) || parsedUses < 0) {
        throw new Error(t("可用次数必须是不小于 0 的整数。"));
      }
      const parsedDays = Number(expiresInDays);
      if (!Number.isInteger(parsedDays) || parsedDays < 0) {
        throw new Error(t("有效期必须是不小于 0 的整数。"));
      }
      return createRedeemCodes({
        amount_micro: amountMicro,
        max_uses: parsedUses,
        expires_in_days: parsedDays,
        note,
        count: parsedCount,
      });
    },
    onSuccess: (batch) => {
      onSuccess(batch.codes);
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  return (
    <>
      <div className="border-b border-rule px-5 py-4">
        <DialogTitle>{t("生成兑换码")}</DialogTitle>
        <p className="mt-1 text-xs text-ink-faint">
          {t("生成用于余额充值的兑换码。码只显示一次，请生成后立即分发。")}
        </p>
      </div>
      <div className="space-y-4 px-5 py-4">
        <Fieldset
          label={t("面额 (USDT)")}
          hint={t("每个码可充值的金额，最多六位小数。")}
          htmlFor="redeem-amount"
        >
          <Input
            id="redeem-amount"
            value={amount}
            onChange={(event) => setAmount(event.target.value)}
            inputMode="decimal"
            className="font-mono"
          />
        </Fieldset>
        <Fieldset
          label={t("生成数量")}
          hint={t("一次最多生成 100 个。")}
          htmlFor="redeem-count"
        >
          <Input
            id="redeem-count"
            value={count}
            onChange={(event) => setCount(event.target.value)}
            inputMode="numeric"
            className="font-mono"
          />
        </Fieldset>
        <div className="grid grid-cols-2 gap-3">
          <Fieldset
            label={t("可用次数")}
            hint={t("0 表示不限次数。")}
            htmlFor="redeem-uses"
          >
            <Input
              id="redeem-uses"
              value={maxUses}
              onChange={(event) => setMaxUses(event.target.value)}
              inputMode="numeric"
              className="font-mono"
            />
          </Fieldset>
          <Fieldset
            label={t("有效期 (天)")}
            hint={t("0 表示永不过期。")}
            htmlFor="redeem-expiry"
          >
            <Input
              id="redeem-expiry"
              value={expiresInDays}
              onChange={(event) => setExpiresInDays(event.target.value)}
              inputMode="numeric"
              className="font-mono"
            />
          </Fieldset>
        </div>
        <Fieldset label={t("备注")} htmlFor="redeem-note">
          <Input
            id="redeem-note"
            value={note}
            onChange={(event) => setNote(event.target.value)}
            placeholder={t("例如：社区活动第一批")}
          />
        </Fieldset>
      </div>
      <div className="flex items-center justify-end gap-2 border-t border-rule bg-rail px-5 py-3">
        <Button variant="secondary" onClick={() => onOpenChange(false)}>
          {t("取消")}
        </Button>
        <Button
          variant="primary"
          loading={createMutation.isPending}
          onClick={() => createMutation.mutate()}
        >
          {t("生成")}
        </Button>
      </div>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </>
  );
}

function CreateRedeemDialog({
  open,
  onOpenChange,
  onSuccess,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSuccess: (batch: AdminRedeemCodeItem[]) => void;
}) {
  return (
    <DialogRoot open={open} onOpenChange={onOpenChange}>
      <DialogPortal>
        <DialogOverlay />
        <DialogContent className="flex w-full max-w-md flex-col">
          {/*
           * The content mounts only while the dialog is open, so every mint
           * starts from the defaults: no amount, count or note survives a
           * close, whatever route closed it.
           */}
          {open && <CreateRedeemContent onOpenChange={onOpenChange} onSuccess={onSuccess} />}
        </DialogContent>
      </DialogPortal>
    </DialogRoot>
  );
}
