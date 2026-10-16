import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Plus, RefreshCw, Trash2 } from "lucide-react";
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
import { Select } from "../../components/ui/Select";
import { Table, TableWrap, TBody, TD, TDClip, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { copyToClipboard } from "../../lib/clipboard";
import { formatApiErrorMessage } from "../../lib/error-message";
import { createInvite, deleteInvite, listInvites, listPlans } from "./api";
import type { AdminInvite, AdminPlan, CreateInviteInput } from "./types";

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

export function InvitesPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();

  const [offset, setOffset] = useState(0);
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<AdminInvite | null>(null);
  const [copiedId, setCopiedId] = useState<string | null>(null);

  const {
    data: invitesData,
    isLoading,
    isError,
    error,
    refetch,
    isFetching,
  } = useQuery({
    queryKey: ["admin-invites", offset],
    queryFn: () => listInvites({ limit: PAGE_SIZE, offset }),
  });

  const { data: plansData } = useQuery({
    queryKey: ["admin-plans-list"],
    queryFn: () => listPlans(),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteInvite(id),
    onSuccess: () => {
      showToast("success", t("邀请码已删除"));
      setDeleteTarget(null);
      void queryClient.invalidateQueries({ queryKey: ["admin-invites"] });
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  const invites = invitesData?.items ?? [];
  const total = invitesData?.total ?? 0;
  const currentPage = Math.floor(offset / PAGE_SIZE) + 1;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  const handleCopy = (code: string, id: string) => {
    // On a plain-HTTP page `navigator.clipboard` is undefined; the guard lives in
    // `copyToClipboard`, so this reports the failure instead of throwing.
    void copyToClipboard(code).then((ok) => {
      if (!ok) {
        showToast("error", t("复制失败，请手动选择复制"));
        return;
      }
      setCopiedId(id);
      showToast("success", t("邀请码已复制到剪贴板"));
      setTimeout(() => setCopiedId(null), 2000);
    });
  };

  const [nowNs] = useState(() => Date.now() * NANOS_PER_MILLI);

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("邀请码管理")}
        description={t("创建与分发新用户注册邀请码，可绑定默认套餐和有效次数。")}
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
              <span>{t("创建邀请码")}</span>
            </Button>
          </div>
        }
      />

      <Panel>
        <PanelHeader
          title={t("全部邀请码")}
          meta={<Badge tone="neutral">{total}</Badge>}
        />

        <PanelBody className="p-0">
          {isLoading ? (
            <LoadingState label={t("正在加载邀请码列表...")} />
          ) : isError ? (
            <ErrorState
              message={formatApiErrorMessage(error, t)}
              onRetry={() => void refetch()}
            />
          ) : invites.length === 0 ? (
            <EmptyState
              title={t("暂无邀请码")}
              hint={t("尚未创建任何注册邀请码，点击右上角创建。")}
              action={
                <Button variant="primary" onClick={() => setCreateOpen(true)}>
                  <Plus size={14} />
                  <span>{t("创建邀请码")}</span>
                </Button>
              }
            />
          ) : (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>{t("邀请码")}</TH>
                    <TH>{t("状态")}</TH>
                    <TH>{t("绑定套餐")}</TH>
                    <TH>{t("使用次数")}</TH>
                    <TH>{t("到期时间")}</TH>
                    <TH>{t("备注")}</TH>
                    <TH>{t("创建时间")}</TH>
                    <TH className="text-right">{t("操作")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {invites.map((invite) => {
                    const isExpired =
                      invite.expires_at_ns > 0 && nowNs > invite.expires_at_ns;
                    const isExhausted =
                      invite.max_uses > 0 &&
                      invite.used_count >= invite.max_uses;
                    const isDisabled = invite.status === "disabled";

                    let badgeTone: "live" | "warn" | "alert" = "live";
                    let badgeLabel = t("有效");

                    /*
                     * Expiry and exhaustion are checked before the status flag,
                     * because the server also marks an expanded-out invite
                     * `disabled` — testing `isDisabled` first collapsed all three
                     * states into "已停用" and the two other labels were dead code.
                     */
                    if (isExpired) {
                      badgeTone = "alert";
                      badgeLabel = t("已过期");
                    } else if (isExhausted) {
                      badgeTone = "warn";
                      badgeLabel = t("已用尽");
                    } else if (isDisabled) {
                      badgeTone = "alert";
                      badgeLabel = t("已停用");
                    }

                    return (
                      <TR key={invite.id}>
                        <TD>
                          <div className="flex items-center gap-2">
                            <span className="font-mono text-sm font-semibold tracking-wider text-ink">
                              {invite.code}
                            </span>
                            <Button
                              variant="ghost"
                              size="icon"
                              onClick={() => handleCopy(invite.code, invite.id)}
                              title={t("复制邀请码")}
                            >
                              {copiedId === invite.id ? (
                                <Check size={14} className="text-signal" />
                              ) : (
                                <Copy size={14} className="text-ink-soft" />
                              )}
                            </Button>
                          </div>
                        </TD>
                        <TD>
                          <Badge tone={badgeTone}>{badgeLabel}</Badge>
                        </TD>
                        <TD className="text-sm text-ink">
                          <TDClip>{invite.plan_name || t("未绑定套餐")}</TDClip>
                        </TD>
                        <TD className="readout text-xs">
                          {invite.used_count} /{" "}
                          {invite.max_uses > 0 ? invite.max_uses : t("不限")}
                        </TD>
                        <TD className="readout text-xs">
                          {invite.expires_at_ns > 0
                            ? formatNsTime(invite.expires_at_ns)
                            : t("永不过期")}
                        </TD>
                        <TD className="text-xs text-ink-soft">
                          <TDClip>{invite.note || "-"}</TDClip>
                        </TD>
                        <TD className="readout text-xs">
                          {formatNsTime(invite.created_at_ns)}
                        </TD>
                        <TD className="text-right">
                          <Button
                            variant="ghost"
                            onClick={() => setDeleteTarget(invite)}
                            title={t("删除邀请码")}
                          >
                            <Trash2 size={14} className="text-alert" />
                            <span>{t("删除")}</span>
                          </Button>
                        </TD>
                      </TR>
                    );
                  })}
                </TBody>
              </Table>
            </TableWrap>
          )}
        </PanelBody>

        {totalPages > 1 && (
          <PanelFooter className="flex items-center justify-between">
            <span className="readout text-xs text-ink-soft">
              {t("第 {{current}} / {{total}} 页 (共 {{count}} 条)", {
                current: currentPage,
                total: totalPages,
                count: total,
              })}
            </span>
            <div className="flex items-center gap-2">
              <Button
                variant="secondary"
                disabled={offset === 0}
                onClick={() =>
                  setOffset((prev) => Math.max(0, prev - PAGE_SIZE))
                }
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
        )}
      </Panel>

      <CreateInviteDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        plans={plansData?.items ?? []}
        onSuccess={() => {
          showToast("success", t("邀请码创建成功"));
          setCreateOpen(false);
          void queryClient.invalidateQueries({ queryKey: ["admin-invites"] });
        }}
      />

      <DialogRoot
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
      >
        <DialogPortal>
          <DialogOverlay />
          <DialogContent className="w-full max-w-md p-6">
            <DialogTitle className="text-base font-semibold text-ink">
              {t("确认删除邀请码")}
            </DialogTitle>
            <p className="mt-3 text-sm text-ink-soft">
              {t(
                "确定要删除邀请码 {{code}} 吗？删除后该邀请码将无法再被用于注册。",
                {
                  code: deleteTarget?.code ?? "",
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
                variant="primary"
                onClick={() => {
                  if (deleteTarget) deleteMutation.mutate(deleteTarget.id);
                }}
                disabled={deleteMutation.isPending}
              >
                {deleteMutation.isPending ? t("正在删除...") : t("确认删除")}
              </Button>
            </div>
          </DialogContent>
        </DialogPortal>
      </DialogRoot>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </div>
  );
}

function CreateInviteDialog({
  open,
  onOpenChange,
  plans,
  onSuccess,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  plans: AdminPlan[];
  onSuccess: () => void;
}) {
  const { t } = useI18n();
  const { toasts, showToast, dismissToast } = useToast();

  /*
   * The dialog stays mounted (it is rendered by the page, not created per open),
   * so state initialised once survived every close: the next "create" dialog
   * opened pre-filled with the previous note, plan, uses and expiry.
   */
  const [planId, setPlanId] = useState("");
  const [maxUses, setMaxUses] = useState("1");
  const [expiresInDays, setExpiresInDays] = useState("7");
  const [note, setNote] = useState("");

  const resetForm = () => {
    setPlanId("");
    setMaxUses("1");
    setExpiresInDays("7");
    setNote("");
  };

  /** Closing by any route (cancel, Esc, backdrop) resets the form. */
  const handleOpenChange = (next: boolean) => {
    if (!next) resetForm();
    onOpenChange(next);
  };

  const activePlans = plans.filter((p) => p.status === "active");

  const createMutation = useMutation({
    mutationFn: async () => {
      const payload: CreateInviteInput = {
        plan_id: planId.trim() || undefined,
        max_uses: Math.max(0, Number(maxUses) || 0),
        expires_in_days:
          expiresInDays.trim() !== ""
            ? Math.max(0, Number(expiresInDays) || 0)
            : undefined,
        note: note.trim() || undefined,
      };

      return createInvite(payload);
    },
    onSuccess: () => {
      resetForm();
      onSuccess();
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  return (
    <DialogRoot open={open} onOpenChange={handleOpenChange}>
      <DialogPortal>
        <DialogOverlay />
        <DialogContent className="w-full max-w-md p-6">
          <DialogTitle className="text-base font-semibold text-ink">
            {t("创建注册邀请码")}
          </DialogTitle>
          <p className="mt-1 text-xs text-ink-soft">
            {t("生成一个可在注册时使用的邀请码，可预先绑定订阅套餐。")}
          </p>

          <div className="mt-5 space-y-4">
            <Fieldset
              label={t("绑定套餐 (可选)")}
              hint={t("用户使用此邀请码注册后将自动获得该套餐")}
              htmlFor="invite-plan"
            >
              <Select
                id="invite-plan"
                value={planId}
                onChange={(e) => setPlanId(e.target.value)}
              >
                <option value="">{t("暂不绑定套餐")}</option>
                {activePlans.map((plan) => (
                  <option key={plan.id} value={plan.id}>
                    {plan.name}
                  </option>
                ))}
              </Select>
            </Fieldset>

            <Fieldset
              label={t("最大可用次数")}
              hint={t("填 0 为不限制使用次数")}
              htmlFor="invite-max-uses"
            >
              <Input
                id="invite-max-uses"
                type="number"
                min="0"
                value={maxUses}
                onChange={(e) => setMaxUses(e.target.value)}
              />
            </Fieldset>

            <Fieldset
              label={t("有效天数 (可选)")}
              hint={t("留空或填 0 表示永不过期")}
              htmlFor="invite-expires"
            >
              <Input
                id="invite-expires"
                type="number"
                min="0"
                value={expiresInDays}
                onChange={(e) => setExpiresInDays(e.target.value)}
                placeholder={t("例如: 7")}
              />
            </Fieldset>

            <Fieldset
              label={t("备注信息 (可选)")}
              hint={t("仅管理员可见，便于追踪邀请来源")}
              htmlFor="invite-note"
            >
              <Input
                id="invite-note"
                value={note}
                onChange={(e) => setNote(e.target.value)}
                placeholder={t("例如: 发给合作团队测试")}
              />
            </Fieldset>
          </div>

          <div className="mt-6 flex items-center justify-end gap-3">
            <Button
              variant="secondary"
              onClick={() => handleOpenChange(false)}
              disabled={createMutation.isPending}
            >
              {t("取消")}
            </Button>
            <Button
              variant="primary"
              onClick={() => createMutation.mutate()}
              disabled={createMutation.isPending}
            >
              {createMutation.isPending ? t("正在创建...") : t("生成邀请码")}
            </Button>
          </div>
        </DialogContent>
      </DialogPortal>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </DialogRoot>
  );
}
