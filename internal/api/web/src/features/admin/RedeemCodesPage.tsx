import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Plus, RefreshCw, Trash2 } from "lucide-react";
import { useState } from "react";
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
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Table, TableWrap, TBody, TD, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { copyToClipboard } from "../../lib/clipboard";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatMicro, parseAmountToMicro } from "../../lib/money";
import { createRedeemCodes, deleteRedeemCode, listRedeemCodes } from "./api";
import type { AdminRedeemCode, AdminRedeemCodeItem } from "./types";

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
 * RedeemCodesPage mints and withdraws top-up codes.
 *
 * The plaintext of a code exists exactly once: in the response that created it.
 * Everything after that shows only the hint, because the database stores an HMAC
 * and there is nothing to show. That is why minting opens a panel with the codes
 * and tells the operator to copy them now.
 */
export function RedeemCodesPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();

  const [offset, setOffset] = useState(0);
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<AdminRedeemCode | null>(null);
  const [minted, setMinted] = useState<AdminRedeemCodeItem[]>([]);

  const [amount, setAmount] = useState("10");
  const [count, setCount] = useState("1");
  const [maxUses, setMaxUses] = useState("1");
  const [expiresInDays, setExpiresInDays] = useState("0");
  const [note, setNote] = useState("");

  const {
    data: codesData,
    isLoading,
    isError,
    error,
    refetch,
    isFetching,
  } = useQuery({
    queryKey: ["admin-redeem-codes", offset],
    queryFn: () => listRedeemCodes({ limit: PAGE_SIZE, offset }),
  });

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
      setMinted(batch.codes);
      setCreateOpen(false);
      showToast("success", t("兑换码已生成，请立即复制保存"));
      void queryClient.invalidateQueries({ queryKey: ["admin-redeem-codes"] });
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
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

  const codes = codesData?.items ?? [];
  const total = codesData?.total ?? 0;
  const currentPage = Math.floor(offset / PAGE_SIZE) + 1;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

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
    <div className="space-y-6">
      <PageHeader
        title={t("兑换码")}
        description={t("生成用于余额充值的兑换码。码只显示一次，请生成后立即分发。")}
        actions={
          <div className="flex items-center gap-2">
            <Button
              variant="secondary"
              onClick={() => void refetch()}
              disabled={isFetching}
              title={t("刷新")}
            >
              <RefreshCw
                size={14}
                className={isFetching ? "animate-spin" : undefined}
              />
              <span>{t("刷新")}</span>
            </Button>
            <Button variant="primary" onClick={() => setCreateOpen(true)}>
              <Plus size={14} />
              <span>{t("生成兑换码")}</span>
            </Button>
          </div>
        }
      />

      {minted.length > 0 && (
        <Panel>
          <PanelHeader
            title={t("新生成的兑换码")}
            description={t("这些码不会再显示第二次。关闭此面板后将无法找回。")}
            meta={<Badge tone="warn">{minted.length}</Badge>}
            actions={
              <Button variant="secondary" onClick={handleCopyAll}>
                <Copy size={14} />
                <span>{t("全部复制")}</span>
              </Button>
            }
          />
          <PanelBody className="space-y-2">
            {minted.map((code) => (
              <div
                key={code.id}
                className="flex items-center justify-between gap-3 rounded-control border border-rule bg-paper-inset px-3 py-2"
              >
                <span className="font-mono text-sm tracking-wider text-ink">
                  {code.code}
                </span>
                <span className="font-mono text-xs text-ink-faint">
                  {formatMicro(code.amount_micro)} USDT
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

      <Panel>
        <PanelHeader
          title={t("兑换码列表")}
          description={t("已生成的兑换码只显示末四位，用于辨认与作废。")}
          meta={<Badge tone="neutral">{total}</Badge>}
        />
        <PanelBody className="p-0">
          {isLoading ? (
            <LoadingState label={t("正在加载兑换码...")} className="py-12" />
          ) : isError ? (
            <ErrorState
              message={formatApiErrorMessage(error, t)}
              onRetry={() => void refetch()}
              className="py-12"
            />
          ) : codes.length === 0 ? (
            <EmptyState
              title={t("暂无兑换码")}
              hint={t("生成兑换码后，可在这里查看使用情况与作废。")}
              className="py-12"
            />
          ) : (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>{t("末四位")}</TH>
                    <TH className="text-right">{t("面额")}</TH>
                    <TH className="text-right">{t("已用 / 次数")}</TH>
                    <TH>{t("状态")}</TH>
                    <TH>{t("过期时间")}</TH>
                    <TH>{t("备注")}</TH>
                    <TH className="text-right">{t("操作")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {codes.map((code) => (
                    <TR key={code.id}>
                      <TD className="font-mono text-xs text-ink">
                        ****{code.code_hint}
                      </TD>
                      <TD className="readout text-right font-mono text-xs text-ink">
                        {formatMicro(code.amount_micro)}
                      </TD>
                      <TD className="readout text-right font-mono text-xs text-ink-soft">
                        {code.used_count} /{" "}
                        {code.max_uses > 0 ? code.max_uses : t("无限制")}
                      </TD>
                      <TD>
                        <Badge
                          tone={code.status === "active" ? "signal" : "neutral"}
                        >
                          {code.status === "active" ? t("可用") : t("已失效")}
                        </Badge>
                      </TD>
                      <TD className="readout font-mono text-xs text-ink-soft">
                        {code.expires_at_ns > 0
                          ? formatNsTime(code.expires_at_ns)
                          : t("永不过期")}
                      </TD>
                      <TD
                        className="max-w-xs truncate text-xs text-ink-faint"
                        title={code.note}
                      >
                        {code.note || "-"}
                      </TD>
                      <TD className="text-right">
                        <Button
                          variant="secondary"
                          onClick={() => setDeleteTarget(code)}
                          title={t("作废此兑换码")}
                        >
                          <Trash2 size={14} />
                          <span>{t("作废")}</span>
                        </Button>
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
          )}
        </PanelBody>
        {totalPages > 1 && (
          <PanelFooter className="flex items-center justify-between">
            <span className="text-xs text-ink-faint">
              {t("第 {{page}} / {{total}} 页", {
                page: currentPage,
                total: totalPages,
              })}
            </span>
            <div className="flex items-center gap-2">
              <Button
                variant="secondary"
                onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
                disabled={offset === 0}
              >
                {t("上一页")}
              </Button>
              <Button
                variant="secondary"
                onClick={() => setOffset(offset + PAGE_SIZE)}
                disabled={currentPage >= totalPages}
              >
                {t("下一页")}
              </Button>
            </div>
          </PanelFooter>
        )}
      </Panel>

      <DialogRoot open={createOpen} onOpenChange={setCreateOpen}>
        <DialogPortal>
          <DialogOverlay />
          <DialogContent>
            <DialogTitle>{t("生成兑换码")}</DialogTitle>
            <div className="space-y-4">
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
              <Fieldset label={t("备注")} htmlFor="redeem-note">
                <Input
                  id="redeem-note"
                  value={note}
                  onChange={(event) => setNote(event.target.value)}
                  placeholder={t("例如：社区活动第一批")}
                />
              </Fieldset>
            </div>
            <div className="mt-6 flex items-center justify-end gap-2">
              <Button variant="secondary" onClick={() => setCreateOpen(false)}>
                {t("取消")}
              </Button>
              <Button
                variant="primary"
                onClick={() => createMutation.mutate()}
                disabled={createMutation.isPending}
              >
                {createMutation.isPending ? t("生成中...") : t("生成")}
              </Button>
            </div>
          </DialogContent>
        </DialogPortal>
      </DialogRoot>

      <DialogRoot
        open={deleteTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteTarget(null);
        }}
      >
        <DialogPortal>
          <DialogOverlay />
          <DialogContent>
            <DialogTitle>{t("作废兑换码")}</DialogTitle>
            <p className="mt-2 text-sm text-ink-soft">
              {t("作废后该码无法再被兑换。已充值的余额不受影响。")}
            </p>
            {deleteTarget && (
              <p className="mt-3 font-mono text-sm text-ink">
                ****{deleteTarget.code_hint} ·{" "}
                {formatMicro(deleteTarget.amount_micro)} USDT
              </p>
            )}
            <div className="mt-6 flex items-center justify-end gap-2">
              <Button variant="secondary" onClick={() => setDeleteTarget(null)}>
                {t("取消")}
              </Button>
              <Button
                variant="primary"
                onClick={() => {
                  if (deleteTarget) deleteMutation.mutate(deleteTarget.id);
                }}
                disabled={deleteMutation.isPending}
              >
                {deleteMutation.isPending ? t("作废中...") : t("确认作废")}
              </Button>
            </div>
          </DialogContent>
        </DialogPortal>
      </DialogRoot>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </div>
  );
}
