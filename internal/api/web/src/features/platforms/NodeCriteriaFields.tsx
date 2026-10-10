import { useQuery } from "@tanstack/react-query";
import { ChevronDown, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useWatch, type UseFormReturn } from "react-hook-form";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Checkbox } from "../../components/ui/Checkbox";
import { Fieldset, Textarea } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { INTEL_CHECK_OUTCOMES, listIntelChecks } from "../intel/checks";
import { listPlatformNodeFacets, previewPlatformScope } from "./api";
import {
  defaultPlatformFormValues,
  listValues,
  toPlatformCriteria,
  type PlatformFormValues,
} from "./formModel";
import type { PlatformNodeFacets } from "./types";

/**
 * The criteria the platform form offers.
 *
 * Every criterion the operator sets must hold at the same time (AND), and the
 * values inside one criterion are alternatives (OR). The help text under the
 * heading says exactly that, because a filter form that implies the wrong
 * combination is worse than no form at all.
 */

/** Network types the quality model emits (`internal/intel/snapshot.go`). */
const ipTypeOptions = [
  "residential",
  "mobile",
  "business",
  "wireless",
  "datacenter",
  "non_residential",
  "unknown",
  "conflicting",
] as const;

/** Purity bands the quality model emits; "review" selects the review verdicts. */
const purityBandOptions = ["excellent", "clean", "fair", "mixed", "poor", "unknown", "review"] as const;

/** Protocol labels mirror the node list, so both pages name a protocol the same way. */
const protocolLabels: Record<string, string> = {
  shadowsocks: "Shadowsocks",
  vmess: "VMess",
  vless: "VLESS",
  trojan: "Trojan",
  hysteria: "Hysteria",
  hysteria2: "Hysteria 2",
  tuic: "TUIC",
  wireguard: "WireGuard",
  shadowtls: "ShadowTLS",
  socks: "SOCKS",
  http: "HTTP",
  ssh: "SSH",
  anytls: "AnyTLS",
  direct: "Direct",
};

const ipTypeLabels: Record<string, string> = {
  residential: "住宅",
  mobile: "移动",
  business: "企业",
  wireless: "无线",
  datacenter: "机房",
  non_residential: "非住宅",
  unknown: "未知",
  conflicting: "冲突",
};

const purityBandLabels: Record<string, string> = {
  excellent: "极佳",
  clean: "干净",
  fair: "一般",
  mixed: "混合",
  poor: "较差",
  unknown: "未评估",
  review: "人工复核",
};

/** Exclusion counters rendered when a criterion is what keeps nodes out. */
const exclusionLabels: Record<string, string> = {
  NODE_DISABLED: "订阅被禁用",
  NODE_UNHEALTHY: "无可用出口或已熔断",
  TAG_FILTER: "标签正则规则",
  EGRESS_UNKNOWN: "未获取到出口 IP",
  REGION_FILTER: "地区",
  SUBSCRIPTION_FILTER: "订阅",
  PROTOCOL_FILTER: "协议",
  IP_TYPE_FILTER: "网络类型",
  PURITY_BAND_FILTER: "纯净度",
  LATENCY_UNKNOWN: "没有延迟记录",
  QUALITY_REJECTED: "质量策略",
};

/** The reason prefixes that all mean "the quality policy rejected the node". */
const qualityReasonPrefix = "QUALITY_";

/** One pickable value of a criterion. */
type CriterionOption = {
  value: string;
  label: string;
  hint?: string;
};

/**
 * mergeOptions keeps the option order stable and appends the values that are
 * selected but missing from the source list (a value from an older inventory, or
 * one the pool no longer carries), so an edit never silently drops a criterion.
 */
function mergeOptions(options: CriterionOption[], selected: string[]): CriterionOption[] {
  const known = new Set(options.map((option) => option.value));
  const extra = selected
    .filter((value) => !known.has(value))
    .map((value) => ({ value, label: value, hint: "已不在当前清单中" }));
  return [...options, ...extra];
}

function CheckboxList({
  idPrefix,
  name,
  options,
  selected,
  onChange,
}: {
  idPrefix: string;
  name: string;
  options: CriterionOption[];
  selected: string[];
  onChange: (next: string[]) => void;
}) {
  const { t } = useI18n();
  const toggle = (value: string) => {
    onChange(selected.includes(value) ? selected.filter((item) => item !== value) : [...selected, value]);
  };

  return (
    <div
      role="group"
      aria-label={name}
      className="max-h-44 overflow-y-auto rounded-control border border-rule bg-paper-raised p-1.5"
    >
      {options.length === 0 ? (
        <p className="px-2 py-1 text-xs text-ink-faint">{t("当前节点池里没有可选值")}</p>
      ) : (
        <ul className="space-y-0.5">
          {options.map((option) => {
            const id = `${idPrefix}-${name}-${option.value}`;
            const checked = selected.includes(option.value);
            return (
              <li key={option.value}>
                <label
                  htmlFor={id}
                  className={cn(
                    "flex cursor-pointer items-center gap-2 rounded-control px-1.5 py-1 text-xs",
                    checked ? "bg-signal-wash text-signal-deep" : "text-ink-soft hover:bg-paper-sunk",
                  )}
                >
                  <Checkbox
                    id={id}
                    checked={checked}
                    onChange={() => toggle(option.value)}
                  />
                  <span className="min-w-0 flex-1 truncate">{option.label}</span>
                  {option.hint ? <span className="shrink-0 text-2xs text-ink-faint">{option.hint}</span> : null}
                </label>
              </li>
            );
          })}
        </ul>
      )}
      <p className="px-1.5 pt-1 text-2xs text-ink-faint">
        {selected.length === 0
          ? t("不限（任何值都通过）")
          : t("已选 {{count}} 项；命中任意一项即通过本项", { count: selected.length })}
      </p>
    </div>
  );
}

function CriterionField({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1">
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-xs font-medium text-ink-soft">{label}</span>
      </div>
      {children}
      {hint ? <p className="text-2xs text-ink-faint">{hint}</p> : null}
    </div>
  );
}

/** The live preview: how many nodes the current criteria would load. */
function ScopePreview({ criteriaKey, criteria }: { criteriaKey: string; criteria: PlatformFormValues }) {
  const { t } = useI18n();
  const [debouncedKey, setDebouncedKey] = useState(criteriaKey);

  useEffect(() => {
    const timer = window.setTimeout(() => setDebouncedKey(criteriaKey), 500);
    return () => window.clearTimeout(timer);
  }, [criteriaKey]);

  // The spec is rebuilt from the (debounced) form values below; the key only
  // decides when a new request is worth making.
  const spec = useMemo(() => toPlatformCriteria(criteria), [criteria]);
  const preview = useQuery({
    queryKey: ["platform-scope-preview", debouncedKey],
    queryFn: () => previewPlatformScope(spec),
    enabled: debouncedKey === criteriaKey,
    staleTime: 5_000,
    placeholderData: (previous) => previous,
  });

  if (preview.isError) {
    return (
      <p className="text-xs text-alert">
        {t("预览失败：无法连接后端，保存时仍会校验。")}
      </p>
    );
  }

  const result = preview.data;
  const headline = result
    ? result.truncated
      ? t("匹配 ≥ {{count}} 个节点", { count: result.matched })
      : t("匹配 {{count}} 个节点", { count: result.matched })
    : t("正在计算匹配节点…");

  const exclusions = result
    ? Object.entries(result.excluded_by)
        .sort((a, b) => b[1] - a[1])
        .slice(0, 3)
    : [];

  return (
    <div className="space-y-2 rounded-control border border-rule bg-paper-sunk px-3 py-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium text-ink">{headline}</span>
        {preview.isFetching && debouncedKey === criteriaKey ? (
          <span className="text-2xs text-ink-faint">{t("刷新中…")}</span>
        ) : null}
        {result?.truncated ? (
          <Badge tone="warn">{t("已扫描 {{scanned}} 个节点后停止（上限 {{limit}}），实际可能更多", { scanned: result.scanned, limit: 20000 })}</Badge>
        ) : null}
      </div>

      {result && result.sample.length > 0 ? (
        <div className="flex flex-wrap gap-1">
          {result.sample.map((node) => (
            <Badge key={node.node_hash} tone="outline">
              {node.display_tag || node.node_hash.slice(0, 12)}
              {node.region ? ` · ${node.region.toUpperCase()}` : ""}
              {node.protocol ? ` · ${protocolLabels[node.protocol] ?? node.protocol}` : ""}
            </Badge>
          ))}
          {result.matched > result.sample.length ? (
            <span className="self-center text-2xs text-ink-faint">
              {t("另有 {{count}} 个未显示", { count: result.matched - result.sample.length })}
            </span>
          ) : null}
        </div>
      ) : null}

      {result && result.matched === 0 ? (
        <p className="text-xs text-warn">
          {t("当前条件没有匹配到任何节点；请减少条件或放宽取值。")}
        </p>
      ) : null}

      {exclusions.length > 0 ? (
        <p className="text-2xs text-ink-faint">
          {t("被排除的原因（首个命中的条件）：")}
          {exclusions
            .map(([reason, count]) => {
              const label = reason.startsWith(qualityReasonPrefix)
                ? exclusionLabels.QUALITY_REJECTED
                : exclusionLabels[reason] ?? reason;
              return `${label} ${count}`;
            })
            .join(" · ")}
        </p>
      ) : null}
    </div>
  );
}

export function NodeCriteriaFields({
  form,
  idPrefix,
}: {
  form: UseFormReturn<PlatformFormValues>;
  idPrefix: string;
}) {
  const { t } = useI18n();

  const facetsQuery = useQuery({
    queryKey: ["platform-node-facets"],
    queryFn: listPlatformNodeFacets,
    staleTime: 30_000,
    refetchInterval: 60_000,
  });
  const watched = useWatch({ control: form.control });
  // The criteria are derived from the watched values, so the memo key below and
  // the preview request only change when a criterion actually changes.
  const values: PlatformFormValues = useMemo(
    () => ({ ...defaultPlatformFormValues, ...watched }),
    [watched],
  );

  const criteriaKey = useMemo(() => JSON.stringify(toPlatformCriteria(values)), [values]);

  const facets: PlatformNodeFacets | undefined = facetsQuery.data;
  const regionValues = listValues(values.region_filters_text);
  const positiveRegions = regionValues.filter((value) => !value.startsWith("!"));

  const regionOptions = mergeOptions(
    (facets?.regions ?? []).map((code) => ({
      value: code,
      label: code.toUpperCase(),
    })),
    positiveRegions,
  );
  const protocolOptions = mergeOptions(
    (facets?.protocols ?? []).map((protocol) => ({
      value: protocol,
      label: protocolLabels[protocol] ?? protocol,
    })),
    values.protocols,
  );
  const subscriptionOptions = mergeOptions(
    (facets?.subscriptions ?? []).map((sub) => ({
      value: sub.id,
      label: sub.name || sub.id,
      hint: t("{{count}} 个节点", { count: sub.node_count }),
    })),
    values.subscription_filters,
  );

  return (
    <div className="space-y-3">
      <div className="space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-xs font-medium text-ink-soft">{t("平台加载的节点")}</span>
          <Badge tone="signal">{t("条件同时生效（AND）")}</Badge>
        </div>
        <p className="max-w-[68ch] text-xs text-ink-faint">
          {t("节点必须同时满足下列每一项条件；同一项里勾选多个值时，命中其中任意一个即通过（OR）。某项留空表示不做限制。")}
        </p>
        {facets ? (
          <p className="text-2xs text-ink-faint">
            {facets.truncated
              ? t("选项来自节点池（已扫描 {{scanned}} / {{total}} 个节点）", {
                  scanned: facets.scanned,
                  total: facets.total_nodes,
                })
              : t("选项来自当前节点池（共 {{total}} 个节点）", { total: facets.total_nodes })}
          </p>
        ) : null}
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <CriterionField
          label={t("国家 / 地区")}
          hint={t("取自节点的出口地区（GeoIP 或探测结果）。")}
        >
          <CheckboxList
            idPrefix={idPrefix}
            name="region"
            options={regionOptions}
            selected={positiveRegions}
            onChange={(next) => {
              // Rewrite only the positive lines: a legacy "!xx" exclusion the
              // picker cannot show stays exactly as it is.
              const excluded = regionValues.filter((value) => value.startsWith("!"));
              form.setValue(
                "region_filters_text",
                [...next, ...excluded].join("\n"),
                { shouldDirty: true },
              );
            }}
          />
        </CriterionField>

        <CriterionField label={t("网络类型（ip_type）")} hint={t("取值来自质量评估模型。")}>
          <CheckboxList
            idPrefix={idPrefix}
            name="ip_type"
            options={ipTypeOptions.map((value) => ({
              value,
              label: `${ipTypeLabels[value] ?? value} (${value})`,
            }))}
            selected={values.ip_types}
            onChange={(next) => form.setValue("ip_types", next, { shouldDirty: true })}
          />
        </CriterionField>

        <CriterionField label={t("纯净度等级")} hint={t("未评估的节点不会通过纯净度条件。")}>
          <CheckboxList
            idPrefix={idPrefix}
            name="purity_band"
            options={purityBandOptions.map((value) => ({
              value,
              label: `${purityBandLabels[value] ?? value} (${value})`,
            }))}
            selected={values.purity_bands}
            onChange={(next) => form.setValue("purity_bands", next, { shouldDirty: true })}
          />
        </CriterionField>

        <CriterionField label={t("协议")} hint={t("取自节点池里实际存在的协议。")}>
          <CheckboxList
            idPrefix={idPrefix}
            name="protocol"
            options={protocolOptions}
            selected={values.protocols}
            onChange={(next) => form.setValue("protocols", next, { shouldDirty: true })}
          />
        </CriterionField>

        <CriterionField
          label={t("订阅")}
          hint={t("节点必须仍被所选订阅引用（替代旧的 ^订阅名/ 正则技巧）。")}
        >
          <CheckboxList
            idPrefix={idPrefix}
            name="subscription"
            options={subscriptionOptions}
            selected={values.subscription_filters}
            onChange={(next) => form.setValue("subscription_filters", next, { shouldDirty: true })}
          />
        </CriterionField>

      </div>

      <ScopePreview criteriaKey={criteriaKey} criteria={values} />

      {regionValues.some((value) => value.startsWith("!")) ? (
        <div className="flex flex-wrap items-center gap-1 text-2xs text-ink-faint">
          <span>{t("保留的排除项（旧配置）：")}</span>
          {regionValues
            .filter((value) => value.startsWith("!"))
            .map((value) => (
              <Button
                key={value}
                type="button"
                variant="secondary"
                size="sm"
                className="text-2xs hover:border-alert hover:text-alert"
                onClick={() =>
                  form.setValue(
                    "region_filters_text",
                    listValues(values.region_filters_text)
                      .filter((item) => item !== value)
                      .join("\n"),
                    { shouldDirty: true },
                  )
                }
              >
                {`${t("排除")} ${value.slice(1).toUpperCase()} ×`}
              </Button>
            ))}
        </div>
      ) : null}

      <details className="rounded-control border border-rule bg-paper-raised">
        <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2 text-xs font-medium text-ink-soft">
          <ChevronDown size={14} aria-hidden />
          {t("高级：解锁要求（可选）")}
        </summary>
        <div className="space-y-2 border-t border-rule px-3 py-2">
          <UnlockRequirementsField form={form} />
        </div>
      </details>

      <details className="rounded-control border border-rule bg-paper-raised">
        <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2 text-xs font-medium text-ink-soft">
          <ChevronDown size={14} aria-hidden />
          {t("高级：旧版标签正则规则（可选）")}
        </summary>
        <div className="space-y-2 border-t border-rule px-3 py-2">
          <Fieldset
            label={t("节点名正则过滤规则")}
            htmlFor={`${idPrefix}-regex`}
            hint={t("仅供旧配置使用：普通正则命中其一即可、* 开头必须匹配、! 开头排除；它与上面的条件同时生效（AND）。")}
          >
            <Textarea
              id={`${idPrefix}-regex`}
              rows={4}
              placeholder={t("每行一条正则表达式，例如：\n香港\n*专线\n!失效")}
              {...form.register("regex_filters_text")}
            />
          </Fieldset>
          <p className="text-2xs text-ink-faint">
            {t("新配置请直接使用上面的条件；保留此框是为了让已保存的平台继续按原有规则过滤。")}
          </p>
        </div>
      </details>
    </div>
  );
}

/**
 * UnlockRequirementsField edits `quality_policy.required_checks`.
 *
 * This is the only admission criterion built from unlock results, and it is
 * deliberately empty by default: a missing unlock result is a label on a node,
 * not a reason to keep it out of a platform. An operator who wants the gate
 * picks a rule and the outcome it must have; every rule and outcome the backend
 * accepts comes from the server, because an unknown check id is rejected with
 * 400 rather than ignored.
 */
function UnlockRequirementsField({
  form,
}: {
  form: UseFormReturn<PlatformFormValues>;
}) {
  const { t } = useI18n();
  const checksQuery = useQuery({
    queryKey: ["intel", "checks"],
    queryFn: ({ signal }) => listIntelChecks(signal),
    staleTime: 300_000,
  });
  const text = useWatch({ control: form.control, name: "required_checks_text" }) ?? "";
  const entries = useMemo(() => {
    const out: { checkID: string; outcome: string }[] = [];
    for (const line of listValues(text)) {
      const separator = line.indexOf(":");
      const checkID = (separator < 0 ? line : line.slice(0, separator)).trim();
      if (checkID) {
        out.push({ checkID, outcome: separator < 0 ? "" : line.slice(separator + 1).trim() });
      }
    }
    return out;
  }, [text]);
  const checks = checksQuery.data ?? [];
  const chosen = new Set(entries.map((entry) => entry.checkID));

  const write = (next: { checkID: string; outcome: string }[]) =>
    form.setValue(
      "required_checks_text",
      next.map((entry) => `${entry.checkID}:${entry.outcome}`).join("\n"),
      { shouldDirty: true },
    );

  return (
    <Fieldset
      label={t("解锁要求")}
      hint={t("留空表示不要求任何解锁结果。勾选后，缺少对应检测结果的节点不会进入该平台。")}
    >
      <div className="space-y-2">
        {entries.length === 0 ? (
          <p className="text-2xs text-ink-faint">{t("未设置解锁要求。")}</p>
        ) : (
          <ul className="space-y-1">
            {entries.map((entry) => {
              const rule = checks.find((check) => check.id === entry.checkID);
              const label =
                rule?.name && rule.name !== rule.id ? `${rule.name} (${entry.checkID})` : entry.checkID;
              return (
                <li key={entry.checkID} className="flex items-center gap-1.5">
                  <span className="min-w-0 flex-1 truncate text-xs text-ink-soft" title={entry.checkID}>
                    {label}
                  </span>
                  <Select
                    aria-label={t("要求的结果")}
                    className="w-36 shrink-0"
                    value={entry.outcome}
                    onChange={(event) =>
                      write(
                        entries.map((item) =>
                          item.checkID === entry.checkID
                            ? { ...item, outcome: event.target.value }
                            : item,
                        ),
                      )
                    }
                  >
                    <option value="">{t("任意结果")}</option>
                    {INTEL_CHECK_OUTCOMES.map((outcome) => (
                      <option key={outcome} value={outcome}>
                        {outcome}
                      </option>
                    ))}
                  </Select>
                  <Button
                    type="button"
                    variant="quiet"
                    size="sm"
                    aria-label={t("移除")}
                    onClick={() => write(entries.filter((item) => item.checkID !== entry.checkID))}
                  >
                    <X size={12} />
                  </Button>
                </li>
              );
            })}
          </ul>
        )}
        <Select
          aria-label={t("添加解锁要求")}
          value=""
          onChange={(event) => {
            const checkID = event.target.value;
            if (checkID && !chosen.has(checkID)) {
              write([...entries, { checkID, outcome: "" }]);
            }
          }}
        >
          <option value="">{t("添加解锁要求…")}</option>
          {checks
            .filter((check) => !chosen.has(check.id))
            .map((check) => (
              <option key={check.id} value={check.id}>
                {check.name && check.name !== check.id ? `${check.name} (${check.id})` : check.id}
              </option>
            ))}
        </Select>
      </div>
    </Fieldset>
  );
}
