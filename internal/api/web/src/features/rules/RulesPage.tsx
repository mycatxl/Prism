import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bug, Pencil, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { type FormEvent, useCallback, useMemo, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input, Textarea } from "../../components/ui/Input";
import { Panel, PanelHeader, SectionTitle } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Sheet } from "../../components/ui/Sheet";
import { Table, TableWrap, TBody, TD, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { deleteRule, listRules, resolveRule, upsertRule } from "./api";
import type { ResolveResult, Rule } from "./types";

const EMPTY_RULES: Rule[] = [];
const HEADERS_PREVIEW_LIMIT = 20;

function parseHeaderList(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function RuleHeadersPreview({ rule }: { rule: Rule }) {
  if (!rule.headers.length) {
    return <span className="text-ink-faint">-</span>;
  }

  const displayHeaders = rule.headers.slice(0, HEADERS_PREVIEW_LIMIT);
  const extraCount = rule.headers.length - HEADERS_PREVIEW_LIMIT;

  return (
    <div className="flex flex-wrap gap-1">
      {displayHeaders.map((header) => (
        <Badge key={header} tone="outline">
          {header}
        </Badge>
      ))}
      {extraCount > 0 && <Badge tone="neutral">+{extraCount}</Badge>}
    </div>
  );
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
    <section className="flex flex-col gap-4 px-4 py-5 lg:px-6">
      <header className="min-w-0">
        <h1 className="text-2xl">{t("请求头规则")}</h1>
        <p className="mt-1 max-w-[80ch] text-sm leading-relaxed text-ink-soft">
          {t("为不同地址设置请求头规则，并先测试后应用。")}
        </p>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <Panel>
        <PanelHeader
          title={t("规则列表")}
          description={t("共 {{count}} 条", { count: rules.length })}
          actions={
            <>
              <label htmlFor="rules-search" className="relative block">
                <Search
                  size={13}
                  aria-hidden
                  className="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-ink-faint"
                />
                <Input
                  id="rules-search"
                  className="h-7 w-40 pl-7 text-xs lg:w-52"
                  placeholder={t("搜索规则")}
                  aria-label={t("搜索规则")}
                  value={search}
                  onChange={(event) => setSearch(event.target.value)}
                />
              </label>
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
            </>
          }
        />

        {rulesQuery.isLoading ? <LoadingState label={t("正在加载规则...")} /> : null}

        {rulesQuery.isError ? (
          <div className="p-4">
            <ErrorState message={formatApiErrorMessage(rulesQuery.error, t)} onRetry={() => void rulesQuery.refetch()} />
          </div>
        ) : null}

        {!rulesQuery.isLoading && !rulesQuery.isError && !rules.length ? (
          <EmptyState
            title={t("没有匹配规则")}
            action={
              <Button variant="secondary" size="sm" onClick={() => setCreateModalOpen(true)}>
                <Plus size={15} />
                {t("新建")}
              </Button>
            }
          />
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
                    <TD>
                      <span className="readout text-xs" title={rule.url_prefix}>
                        {rule.url_prefix}
                      </span>
                    </TD>
                    <TD>
                      <RuleHeadersPreview rule={rule} />
                    </TD>
                    <TD>
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
            <Button type="submit" form="rule-edit-form" disabled={updateMutation.isPending}>
              {updateMutation.isPending ? t("保存中...") : t("保存规则")}
            </Button>
          </div>
        }
      >
        <div className="flex flex-col gap-5">
          <section>
            <SectionTitle>{t("规则编辑")}</SectionTitle>
            <p className="text-xs leading-relaxed text-ink-soft">{t("修改地址前缀和请求头后保存。")}</p>

            <form id="rule-edit-form" className="mt-3 flex flex-col gap-3" onSubmit={handleUpdateSubmit}>
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
          </section>

          {selectedRule ? (
            <section className="border-t border-rule pt-4">
              <SectionTitle>{t("运维操作")}</SectionTitle>
              <div className="mt-2 flex items-start justify-between gap-4">
                <div className="min-w-0">
                  <h3 className="text-sm font-medium">{t("删除规则")}</h3>
                  <p className="mt-0.5 max-w-[60ch] text-xs leading-relaxed text-ink-soft">
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
              </div>
            </section>
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
            >
              {resolveMutation.isPending ? t("测试中...") : t("开始测试")}
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
            <p className="flex items-baseline gap-2">
              <span className="text-xs text-ink-soft">{t("命中前缀：")}</span>
              <span className="readout text-xs">{resolveOutput.matched_url_prefix || t("无")}</span>
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
            <Button type="submit" form="create-rule-form" disabled={createMutation.isPending}>
              {createMutation.isPending ? t("创建中...") : t("确认创建")}
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
    </section>
  );
}