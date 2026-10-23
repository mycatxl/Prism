import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
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
import { adminPlansQueryKey, createInvite, deleteInvite, listInvites, listPlans } from "./api";
import type { AdminInvite, AdminPlan, CreateInviteInput } from "./types";

/*
 * The operator's invite codes, on the Signal Console rhythm: the page header
 * states the page, carries the total and the one action that creates, and one
 * panel carries a toolbar of search + state + refresh over a single data grid.
 * The status column is the dot-and-word pair, the figures a reader compares are
 * readouts, and the code itself is mono with its copy beside it.
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
 * The state an invite is in, as the registration path will treat it.
 *
 * Expiry and exhaustion are checked before the stored status flag, because the
 * server also reports an expanded-out invite as `disabled` — testing
 * `disabled` first collapsed all three states into "已停用" and left the two
 * other labels dead code.
 */
type InviteState = "usable" | "exhausted" | "expired" | "disabled";
type InviteFilter = "all" | InviteState;

function classifyInvite(invite: AdminInvite, nowNs: number): InviteState {
  if (invite.expires_at_ns > 0 && nowNs > invite.expires_at_ns) return "expired";
  if (invite.max_uses > 0 && invite.used_count >= invite.max_uses) return "exhausted";
  if (invite.status === "disabled") return "disabled";
  return "usable";
}

/** The `.state-dot` variant for each invite state; `usable` is the live one. */
const INVITE_DOT: Record<InviteState, string> = {
  usable: "state-dot state-dot--signal",
  exhausted: "state-dot state-dot--warn",
  expired: "state-dot state-dot--alert",
  disabled: "state-dot state-dot--alert",
};

export function InvitesPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();

  const [offset, setOffset] = useState(0);
  const [search, setSearch] = useState("");
  const [stateFilter, setStateFilter] = useState<InviteFilter>("all");
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<AdminInvite | null>(null);
  const [copiedId, setCopiedId] = useState<string | null>(null);

  const {
    data: invitesData,
    isLoading,
    isError,
    error,
    isFetching,
  } = useQuery({
    queryKey: ["admin-invites", offset],
    queryFn: () => listInvites({ limit: PAGE_SIZE, offset }),
  });

  const { data: plansData } = useQuery({
    queryKey: adminPlansQueryKey,
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
  const invites = useMemo(() => invitesData?.items ?? [], [invitesData]);
  const total = invitesData?.total ?? 0;
  const currentPage = Math.floor(offset / PAGE_SIZE) + 1;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  const [nowNs] = useState(() => Date.now() * NANOS_PER_MILLI);

  /*
   * Search and state narrow the rows this page already holds: the needle
   * matches the code, the bound plan and the note — the three things an
   * operator scans a handout list by.
   */
  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return invites.filter((invite) => {
      if (stateFilter !== "all" && classifyInvite(invite, nowNs) !== stateFilter) {
        return false;
      }
      if (!needle) return true;
      const haystack = [invite.code, invite.plan_name, invite.note]
        .join(" ")
        .toLowerCase();
      return haystack.includes(needle);
    });
  }, [invites, search, stateFilter, nowNs]);

  const filtersActive = search.trim() !== "" || stateFilter !== "all";
  const clearFilters = () => {
    setSearch("");
    setStateFilter("all");
  };

  // The page's own query cluster: the list this panel reads and the plan picker
  // the create form reads.
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["admin-invites"] });
    void queryClient.invalidateQueries({ queryKey: adminPlansQueryKey });
  };

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

  return (
    <Page bleed>
      <PageHeader
        title={t("邀请码")}
        description={t("创建与分发新用户注册邀请码，可绑定默认套餐和有效次数。")}
        meta={
          <PageMeta
            label={t("邀请码总数")}
            value={invitesData ? total.toLocaleString() : "--"}
          />
        }
        actions={
          <Button variant="primary" onClick={() => setCreateOpen(true)}>
            <Plus size={16} />
            <span>{t("创建邀请码")}</span>
          </Button>
        }
      />

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <div className="page-content page-content--fill">
        <Panel className="flex min-w-0 flex-col">
          <PanelHeader
            title={t("全部邀请码")}
            meta={invitesData ? t("共 {{count}} 个邀请码", { count: total }) : "--"}
          />

          <PanelToolbar>
            <label htmlFor="invites-search" className="relative block min-w-48 sm:w-64">
              <Search
                size={13}
                aria-hidden
                className="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-ink-faint"
              />
              <Input
                id="invites-search"
                className="pl-7 text-xs"
                placeholder={t("搜索邀请码")}
                aria-label={t("搜索邀请码")}
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
                setStateFilter(event.target.value as InviteFilter);
                setOffset(0);
              }}
            >
              <option value="all">{t("全部状态")}</option>
              <option value="usable">{t("有效")}</option>
              <option value="exhausted">{t("已用尽")}</option>
              <option value="expired">{t("已过期")}</option>
              <option value="disabled">{t("已停用")}</option>
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
              <LoadingState label={t("正在加载邀请码列表...")} />
            </PanelBody>
          ) : isError ? (
            <PanelBody>
              <ErrorState message={formatApiErrorMessage(error, t)} onRetry={refresh} />
            </PanelBody>
          ) : invites.length === 0 ? (
            <PanelBody>
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
            </PanelBody>
          ) : filtered.length === 0 ? (
            <PanelBody>
              <EmptyState
                title={t("没有匹配的邀请码")}
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
                    <TH>{t("邀请码")}</TH>
                    <TH>{t("状态")}</TH>
                    <TH>{t("绑定套餐")}</TH>
                    <TH className="text-right">{t("使用次数")}</TH>
                    <TH>{t("到期时间")}</TH>
                    <TH>{t("备注")}</TH>
                    <TH>{t("创建时间")}</TH>
                    <TH className="text-right">{t("操作")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {filtered.map((invite) => {
                    const state = classifyInvite(invite, nowNs);
                    const maxUses =
                      invite.max_uses > 0 ? invite.max_uses : t("不限");
                    return (
                      <TR key={invite.id}>
                        <TD>
                          <div className="flex items-center gap-2">
                            <span className="mono text-sm font-semibold tracking-wider text-ink">
                              {invite.code}
                            </span>
                            <Button
                              variant="ghost"
                              size="icon"
                              onClick={() => handleCopy(invite.code, invite.id)}
                              title={t("复制邀请码")}
                              aria-label={t("复制邀请码")}
                            >
                              {copiedId === invite.id ? (
                                <Check size={14} className="text-signal" />
                              ) : (
                                <Copy size={14} className="text-ink-faint" />
                              )}
                            </Button>
                          </div>
                        </TD>
                        <TD className="whitespace-nowrap">
                          <span className="inline-flex items-center gap-2">
                            <span aria-hidden className={INVITE_DOT[state]} />
                            <span className="state-text">
                              {state === "usable" && t("有效")}
                              {state === "exhausted" && t("已用尽")}
                              {state === "expired" && t("已过期")}
                              {state === "disabled" && t("已停用")}
                            </span>
                          </span>
                        </TD>
                        <TDClip className="text-sm text-ink">
                          {invite.plan_name || t("未绑定套餐")}
                        </TDClip>
                        <TDNum className="whitespace-nowrap">
                          <span className="readout">
                            {invite.used_count}
                            <small> / {maxUses}</small>
                          </span>
                        </TDNum>
                        <TD className="whitespace-nowrap text-xs text-ink-soft">
                          {invite.expires_at_ns > 0
                            ? formatNsTime(invite.expires_at_ns)
                            : t("永不过期")}
                        </TD>
                        <TDClip className="text-xs text-ink-faint">
                          {invite.note || "-"}
                        </TDClip>
                        <TD className="whitespace-nowrap readout text-xs text-ink-soft">
                          {formatNsTime(invite.created_at_ns)}
                        </TD>
                        <TD className="text-right">
                          <Button
                            variant="ghost"
                            size="icon"
                            className="text-ink-faint hover:bg-alert-wash hover:text-alert"
                            onClick={() => setDeleteTarget(invite)}
                            title={t("删除邀请码")}
                            aria-label={t("删除邀请码")}
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
          <DialogContent className="w-full max-w-md">
            <div className="border-b border-rule px-5 py-4">
              <DialogTitle>{t("确认删除邀请码")}</DialogTitle>
            </div>
            <div className="px-5 py-4 text-sm leading-relaxed text-ink-soft">
              {t(
                "确定要删除邀请码 {{code}} 吗？删除后该邀请码将无法再被用于注册。",
                {
                  code: deleteTarget?.code ?? "",
                },
              )}
            </div>
            <div className="flex items-center justify-end gap-2 border-t border-rule bg-rail px-5 py-3">
              <Button
                variant="secondary"
                onClick={() => setDeleteTarget(null)}
                disabled={deleteMutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button
                variant="danger"
                loading={deleteMutation.isPending}
                onClick={() => {
                  if (deleteTarget) deleteMutation.mutate(deleteTarget.id);
                }}
              >
                {t("确认删除")}
              </Button>
            </div>
          </DialogContent>
        </DialogPortal>
      </DialogRoot>
    </Page>
  );
}

function CreateInviteContent({
  onOpenChange,
  plans,
  onSuccess,
}: {
  onOpenChange: (open: boolean) => void;
  plans: AdminPlan[];
  onSuccess: () => void;
}) {
  const { t } = useI18n();
  const { toasts, showToast, dismissToast } = useToast();

  const [planId, setPlanId] = useState("");
  const [maxUses, setMaxUses] = useState("1");
  const [expiresInDays, setExpiresInDays] = useState("7");
  const [note, setNote] = useState("");

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
      onSuccess();
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  return (
    <>
      <div className="border-b border-rule px-5 py-4">
        <DialogTitle>{t("创建注册邀请码")}</DialogTitle>
        <p className="mt-1 text-xs text-ink-faint">
          {t("生成一个可在注册时使用的邀请码，可预先绑定订阅套餐。")}
        </p>
      </div>

      <div className="space-y-4 px-5 py-4">
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

      <div className="flex items-center justify-end gap-2 border-t border-rule bg-rail px-5 py-3">
        <Button variant="secondary" onClick={() => onOpenChange(false)}>
          {t("取消")}
        </Button>
        <Button
          variant="primary"
          loading={createMutation.isPending}
          onClick={() => createMutation.mutate()}
        >
          {t("生成邀请码")}
        </Button>
      </div>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </>
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
  return (
    <DialogRoot open={open} onOpenChange={onOpenChange}>
      <DialogPortal>
        <DialogOverlay />
        <DialogContent className="flex w-full max-w-md flex-col">
          {/*
           * The content mounts only while the dialog is open, so every open
           * starts from a fresh form: no note, plan, uses or expiry survives a
           * close, whatever route closed it.
           */}
          {open && (
            <CreateInviteContent
              onOpenChange={onOpenChange}
              plans={plans}
              onSuccess={onSuccess}
            />
          )}
        </DialogContent>
      </DialogPortal>
    </DialogRoot>
  );
}
