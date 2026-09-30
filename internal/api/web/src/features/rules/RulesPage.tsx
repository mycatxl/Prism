import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bug, Pencil, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { type FormEvent, useCallback, useMemo, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input, Textarea } from "../../components/ui/Input";
import { Page, PageHeader, PageMeta } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelHeader, PanelToolbar } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Sheet } from "../../components/ui/Sheet";
import { Table, TableWrap, TBody, TD, TDClip, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { deleteRule, listRules, resolveRule, upsertRule } from "./api";
import type { ResolveResult, Rule } from "./types";

const EMPTY_RULES: Rule[] = [];
// A rule can carry dozens of headers; the cell shows the first few and keeps the
// full list in its title, because a wrapped badge cloud breaks the row grid.
const HEADERS_PREVIEW_LIMIT = 3;

function parseHeaderList(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function headerPreview(rule: Rule): string {
  const shown = rule.headers.slice(0, HEADERS_PREVIEW_LIMIT).join(", ");
  const extra = rule.headers.length - HEADERS_PREVIEW_LIMIT;
  return extra > 0 ? `${shown} +${extra}` : shown;
}

function isFallbackRule(rule: Rule): boolean {
  return rule.url_prefix === "*";
}

export function RulesPage() {
  const { t } = useI18n();
  const [search, setSearch] = useState("");
  const [selectedPrefix, setSelectedPrefix] = useState("");
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [createModalOpen, setCreateModalOpen] = useState(false);
  const [formPrefix, setFormPrefix] = useState("");
  const [formHeadersRaw, setFormHeadersRaw] = useState("");
  const [createPrefix, setCreatePrefix] = useState("");
  const [createHeadersRaw, setCreateHeadersRaw] = useState("");
  const [resolveModalOpen, setResolveModalOpen] = useState(false);
  const [resolveURL, setResolveURL] = useState("");
  const [resolveOutput, setResolveOutput] = useState<ResolveResult | null>(null);
  const { toasts, showToast, dismissToast } = useToast();

  const queryClient = useQueryClient();

  const rulesQuery = useQuery({
    queryKey: ["header-rules", search],
    queryFn: () => listRules(search),
    refetchInterval: 30_000,
  });

  const rules = rulesQuery.data ?? EMPTY_RULES;

  const selectedRule = useMemo(() => {
    if (!selectedPrefix) {
      return null;
    }
    return rules.find((item) => item.url_prefix === selectedPrefix) ?? null;
  }, [rules, selectedPrefix]);

  const syncFormFromRule = useCallback((rule: Rule) => {
    setFormPrefix(rule.url_prefix);
    setFormHeadersRaw(rule.headers.join("\n"));
    setSelectedPrefix(rule.url_prefix);
  }, []);

  const openDrawerForRule = useCallback((rule: Rule) => {
    syncFormFromRule(rule);
    setDrawerOpen(true);
  }, [syncFormFromRule]);

  const invalidateRules = async () => {
    await queryClient.invalidateQueries({ queryKey: ["header-rules"] });
  };

  const createMutation = useMutation({
    mutationFn: async () => {
      const prefix = createPrefix.trim();
      const headers = parseHeaderList(createHeadersRaw);
      if (!prefix) {
        throw new Error("地址前缀不能为空");
      }
      if (!headers.length) {
        throw new Error("请求头不能为空");
      }
      return upsertRule(prefix, headers);
    },
    onSuccess: async (rule) => {
      await invalidateRules();
      setCreateModalOpen(false);
      setCreatePrefix("");
      setCreateHeadersRaw("");
      showToast("success", t("规则 {{prefix}} 已创建", { prefix: rule.url_prefix }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const updateMutation = useMutation({
    mutationFn: async () => {
      const prefix = formPrefix.trim();
      const headers = parseHeaderList(formHeadersRaw);
      if (!prefix) {
        throw new Error("地址前缀不能为空");
      }
      if (!headers.length) {
        throw new Error("请求头不能为空");
      }
      return upsertRule(prefix, headers);
    },
    onSuccess: async (rule) => {
      await invalidateRules();
      syncFormFromRule(rule);
      showToast("success", t("规则 {{prefix}} 已保存", { prefix: rule.url_prefix }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (prefix: string) => {
      await deleteRule(prefix);
      return prefix;
    },
    onSuccess: async (prefix) => {
      await invalidateRules();
      if (selectedPrefix === prefix) {
        setSelectedPrefix("");
        setDrawerOpen(false);
      }
      showToast("success", t("规则 {{prefix}} 已删除", { prefix }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });
  const deleteRuleMutateAsync = deleteMutation.mutateAsync;
  const isDeletePending = deleteMutation.isPending;

  const resolveMutation = useMutation({
    mutationFn: async () => {
      const targetURL = resolveURL.trim();
      if (!targetURL) {
        throw new Error("请输入 URL");
      }
      return resolveRule(targetURL);
    },
    onSuccess: (result) => {
      setResolveOutput(result);
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const handleDelete = useCallback(async (rule: Rule) => {
    if (isFallbackRule(rule)) {
      showToast("error", '兜底规则 "*" 不允许删除');
      return;
    }
    const confirmed = window.confirm(t("确认删除规则 {{prefix}} 吗？", { prefix: rule.url_prefix }));
    if (!confirmed) {
      return;
    }
    await deleteRuleMutateAsync(rule.url_prefix);
  }, [deleteRuleMutateAsync, showToast, t]);

  const handleUpdateSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    void updateMutation.mutateAsync();
  };

  const handleCreateSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    void createMutation.mutateAsync();
  };

  return (
    <Page bleed>
      <PageHeader
        title={t("请求头规则")}
        description={t("为不同地址设置请求头规则，并先测试后应用。")}
        meta={<PageMeta label={t("规则列表")} value={t("共 {{count}} 条", { count: rules.length })} />}
      />

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <div className="px-4 py-3 lg:px-5 lg:py-4 2xl:px-6 2xl:py-5">
        <Panel className="flex min-w-0 flex-col">
          <PanelHeader
            title={t("规则列表")}
            meta={t("共 {{count}} 条", { count: rules.length })}
          />
          <PanelToolbar>
            <label htmlFor="rules-search" className="relative block min-w-48 sm:w-64">
              <Search
                size={13}
                aria-hidden
                className="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-ink-faint"
              />
              <Input
                id="rules-search"
                className="h-7 pl-7 text-xs"
                placeholder={t("搜索规则")}
                aria-label={t("搜索规则")}
                value={search}
                onChange={(event) => setSearch(event.target.value)}
              />
            </label>
            <div className="ml-auto flex items-center gap-1.5">
              <Button variant="secondary" size="sm" onClick={() => setCreateModalOpen(true)}>
                <Plus size={15} />
                {t("新建")}
              </Button>
              <Button variant="secondary" size="sm" onClick={() => setResolveModalOpen(true)}>
                <Bug size={15} />
                {t("调试")}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => void rulesQuery.refetch()}
                disabled={rulesQuery.isFetching}
              >
                <RefreshCw size={15} className={rulesQuery.isFetching ? "animate-spin" : undefined} />
                {t("刷新")}
              </Button>
            </div>
          </PanelToolbar>

          {rulesQuery.isLoading ? (
            <PanelBody>
              <LoadingState label={t("正在加载规则...")} />
            </PanelBody>
          ) : null}

          {rulesQuery.isError ? (
            <PanelBody>
              <ErrorState message={formatApiErrorMessage(rulesQuery.error, t)} onRetry={() => void rulesQuery.refetch()} />
            </PanelBody>
          ) : null}

          {!rulesQuery.isLoading && !rulesQuery.isError && !rules.length ? (
            <PanelBody>
              <EmptyState
                title={t("没有匹配规则")}
                action={
                  <Button variant="secondary" size="sm" onClick={() => setCreateModalOpen(true)}>
                    <Plus size={15} />
                    {t("新建")}
                  </Button>
                }
              />
            </PanelBody>
          ) : null}

          {rules.length ? (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>{t("URL 前缀")}</TH>
                    <TH>{t("请求头")}</TH>
                    <TH className="text-right">{t("操作")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {rules.map((rule) => (
                    <TR
                      key={rule.url_prefix}
                      tabIndex={0}
                      className="cursor-pointer"
                      aria-selected={selectedPrefix === rule.url_prefix}
                      selected={selectedPrefix === rule.url_prefix}
                      onClick={() => openDrawerForRule(rule)}
                      onKeyDown={(event) => {
                        if (event.target === event.currentTarget && (event.key === "Enter" || event.key === " ")) {
                          event.preventDefault();
                          openDrawerForRule(rule);
                        }
                      }}
                    >
                      <TDClip className="readout text-xs" title={rule.url_prefix}>
                        {rule.url_prefix}
                      </TDClip>
                      <TDClip className="text-xs text-ink-soft" title={rule.headers.join(", ")}>
                        {headerPreview(rule)}
                      </TDClip>
                      <TD className="text-right">
                        <div className="flex items-center justify-end gap-1" onClick={(event) => event.stopPropagation()}>
                          <Button size="icon" variant="ghost" onClick={() => openDrawerForRule(rule)} title={t("编辑")}>
                            <Pencil size={14} />
                          </Button>
                          <Button
                            size="icon"
                            variant="ghost"
                            className="text-ink-faint hover:bg-alert-wash hover:text-alert"
                            onClick={() => void handleDelete(rule)}
                            disabled={isDeletePending || isFallbackRule(rule)}
                            title={isFallbackRule(rule) ? t('兜底规则 "*" 不可删除') : t("删除")}
                          >
                            <Trash2 size={14} />
                          </Button>
                        </div>
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
          ) : null}
        </Panel>
      </div>

      <Sheet
        open={drawerOpen}
        onOpenChange={(open) => {
          if (!open) {
            setDrawerOpen(false);
          }
        }}
        title={selectedRule?.url_prefix || t("规则编辑")}
        description={t("编辑当前规则")}
        width="md"
        footer={
          <div className="flex items-center justify-end gap-2">
            <Button type="submit" form="rule-edit-form" disabled={updateMutation.isPending} loading={updateMutation.isPending}>
              {t("保存规则")}
            </Button>
          </div>
        }
      >
        <div className="flex flex-col gap-4">
          <Panel>
            <PanelHeader title={t("规则编辑")} description={t("修改地址前缀和请求头后保存。")} />
            <PanelBody>
              <form id="rule-edit-form" className="flex flex-col gap-3" onSubmit={handleUpdateSubmit}>
                <Fieldset
                  label={t("地址前缀")}
                  htmlFor="rule-prefix"
                  hint={selectedRule ? t("如需改名，请新建规则后删除当前规则。") : undefined}
                >
                  <Input
                    id="rule-prefix"
                    className="readout"
                    placeholder={t("例如 api.example.com/v1")}
                    value={formPrefix}
                    readOnly={Boolean(selectedRule)}
                    title={selectedRule ? t("已存在规则的地址前缀不可直接改名") : undefined}
                    onChange={(event) => setFormPrefix(event.target.value)}
                  />
                </Fieldset>

                <Fieldset label={t("请求头")} htmlFor="rule-headers">
                  <Textarea
                    id="rule-headers"
                    className="readout text-xs"
                    rows={5}
                    placeholder={t("每行一个 header，例如 Authorization")}
                    value={formHeadersRaw}
                    onChange={(event) => setFormHeadersRaw(event.target.value)}
                  />
                </Fieldset>
              </form>
            </PanelBody>
          </Panel>

          {selectedRule ? (
            <Panel>
              <PanelHeader title={t("运维操作")} />
              <PanelBody className="flex items-start justify-between gap-4">
                <div className="min-w-0">
                  <h3 className="text-sm font-medium">{t("删除规则")}</h3>
                  <p className="mt-0.5 max-w-[68ch] text-xs leading-relaxed text-ink-soft">
                    {isFallbackRule(selectedRule)
                      ? t('兜底规则 "*" 仅允许编辑，不允许删除。')
                      : t("删除后该规则将不再生效。")}
                  </p>
                </div>
                <Button
                  variant="danger"
                  onClick={() => void handleDelete(selectedRule)}
                  disabled={deleteMutation.isPending || isFallbackRule(selectedRule)}
                >
                  {t("删除")}
                </Button>
              </PanelBody>
            </Panel>
          ) : null}
        </div>
      </Sheet>

      <Sheet
        open={resolveModalOpen}
        onOpenChange={(open) => {
          if (!open) {
            setResolveModalOpen(false);
          }
        }}
        title={t("规则测试")}
        description={t("输入地址查看命中规则和请求头。")}
        width="sm"
        footer={
          <div className="flex items-center justify-end gap-2">
            <Button
              variant="secondary"
              onClick={() => void resolveMutation.mutateAsync()}
              disabled={resolveMutation.isPending}
              loading={resolveMutation.isPending}
            >
              {t("开始测试")}
            </Button>
          </div>
        }
      >
        <Fieldset label={t("目标地址")} htmlFor="resolve-url">
          <Input
            id="resolve-url"
            className="readout text-xs"
            placeholder="https://api.example.com/v1/orders/123"
            value={resolveURL}
            onChange={(event) => setResolveURL(event.target.value)}
          />
        </Fieldset>

        {resolveOutput ? (
          <div className="mt-4 flex flex-col gap-2 border-t border-rule pt-3 text-sm">
            <p className="flex min-w-0 items-baseline gap-2">
              <span className="shrink-0 text-xs text-ink-soft">{t("命中前缀：")}</span>
              <span className="readout truncate text-xs" title={resolveOutput.matched_url_prefix || t("无")}>
                {resolveOutput.matched_url_prefix || t("无")}
              </span>
            </p>
            <div className="flex flex-col gap-1.5">
              <span className="text-xs text-ink-soft">{t("命中请求头：")}</span>
              {resolveOutput.headers?.length ? (
                <div className="flex flex-wrap gap-1">
                  {resolveOutput.headers.map((header) => (
                    <Badge key={header} tone="outline">
                      {header}
                    </Badge>
                  ))}
                </div>
              ) : (
                <span className="text-xs text-ink-faint">{t("无")}</span>
              )}
            </div>
          </div>
        ) : null}
      </Sheet>

      <Sheet
        open={createModalOpen}
        onOpenChange={(open) => {
          if (!open) {
            setCreateModalOpen(false);
          }
        }}
        title={t("新建规则")}
        width="sm"
        footer={
          <div className="flex items-center justify-end gap-2">
            <Button variant="secondary" onClick={() => setCreateModalOpen(false)} disabled={createMutation.isPending}>
              {t("取消")}
            </Button>
            <Button type="submit" form="create-rule-form" disabled={createMutation.isPending} loading={createMutation.isPending}>
              {t("确认创建")}
            </Button>
          </div>
        }
      >
        <form id="create-rule-form" className="flex flex-col gap-3" onSubmit={handleCreateSubmit}>
          <Fieldset label={t("地址前缀")} htmlFor="create-rule-prefix">
            <Input
              id="create-rule-prefix"
              className="readout text-xs"
              placeholder={t("例如 api.example.com/v1")}
              value={createPrefix}
              onChange={(event) => setCreatePrefix(event.target.value)}
            />
          </Fieldset>

          <Fieldset label={t("请求头")} htmlFor="create-rule-headers">
            <Textarea
              id="create-rule-headers"
              className="readout text-xs"
              rows={5}
              placeholder={t("每行一个 header，例如 Authorization")}
              value={createHeadersRaw}
              onChange={(event) => setCreateHeadersRaw(event.target.value)}
            />
          </Fieldset>
        </form>
      </Sheet>
    </Page>
  );
}
