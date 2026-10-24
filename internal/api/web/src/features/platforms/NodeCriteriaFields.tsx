import { useQuery } from "@tanstack/react-query";
import { ChevronDown, X } from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useWatch, type UseFormReturn } from "react-hook-form";
import {
  ControlButton,
  ControlCheckbox,
  ControlSelect,
  QueryBadge,
} from "../../components/control-plane";
import { useI18n } from "../../i18n";
import { INTEL_CHECK_OUTCOMES, listIntelChecks } from "../intel/checks";
import { listPlatformNodeFacets, previewPlatformScope } from "./api";
import {
  defaultPlatformFormValues,
  listValues,
  toPlatformCriteria,
  type PlatformFormValues,
} from "./formModel";
import type { PlatformNodeFacets } from "./types";

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

const purityBandOptions = ["excellent", "clean", "fair", "mixed", "poor", "unknown", "review"] as const;

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

const exclusionLabels: Record<string, string> = {
  NODE_DISABLED: "订阅被禁用",
  NODE_UNHEALTHY: "无可用出口或已熔断",
  TAG_FILTER: "旧标签规则",
  EGRESS_UNKNOWN: "未获取到出口 IP",
  REGION_FILTER: "地区",
  SUBSCRIPTION_FILTER: "订阅",
  PROTOCOL_FILTER: "协议",
  IP_TYPE_FILTER: "网络类型",
  PURITY_BAND_FILTER: "纯净度",
  LATENCY_UNKNOWN: "没有延迟记录",
  QUALITY_REJECTED: "质量策略",
};

const qualityReasonPrefix = "QUALITY_";

type CriterionOption = {
  value: string;
  label: string;
  hint?: string;
};

function mergeOptions(options: CriterionOption[], selected: string[]): CriterionOption[] {
  const known = new Set(options.map((option) => option.value));
  const extra = selected
    .filter((value) => !known.has(value))
    .map((value) => ({ value, label: value, hint: "已不在当前清单中" }));
  return [...options, ...extra];
}

function CheckboxList({
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
    <div role="group" aria-label={name} className="cp-criteria-options">
      {options.length === 0 ? (
        <p className="cp-muted">{t("当前节点池里没有可选值")}</p>
      ) : (
        <div className="cp-criteria-options__items">
          {options.map((option) => {
            const checked = selected.includes(option.value);
            return (
              <ControlCheckbox
                key={option.value}
                isSelected={checked}
                onChange={() => toggle(option.value)}
                label={option.label}
                hint={option.hint ? t(option.hint) : undefined}
                className={checked ? "is-selected" : undefined}
              />
            );
          })}
        </div>
      )}
      <p className="cp-criteria-options__summary">
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
  children: ReactNode;
}) {
  return (
    <div className="cp-criteria-field">
      <div className="cp-criteria-field__title">{label}</div>
      {children}
      {hint ? <p className="cp-criteria-field__hint">{hint}</p> : null}
    </div>
  );
}

function ScopePreview({ criteriaKey, criteria }: { criteriaKey: string; criteria: PlatformFormValues }) {
  const { t } = useI18n();
  const [debouncedKey, setDebouncedKey] = useState(criteriaKey);

  useEffect(() => {
    const timer = window.setTimeout(() => setDebouncedKey(criteriaKey), 500);
    return () => window.clearTimeout(timer);
  }, [criteriaKey]);

  const spec = useMemo(() => toPlatformCriteria(criteria), [criteria]);
  const preview = useQuery({
    queryKey: ["platform-scope-preview", debouncedKey],
    queryFn: () => previewPlatformScope(spec),
    enabled: debouncedKey === criteriaKey,
    staleTime: 5_000,
    placeholderData: (previous) => previous,
  });

  if (preview.isError) {
    return <p className="cp-form-error">{t("预览失败：无法连接后端，保存时仍会校验。")}</p>;
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
    <div className="cp-scope-preview" aria-live="polite">
      <div className="cp-scope-preview__head">
        <strong>{headline}</strong>
        {preview.isFetching && debouncedKey === criteriaKey ? <span>{t("刷新中…")}</span> : null}
        {result?.truncated ? (
          <QueryBadge tone="warn" label={t("扫描结果为下限")} />
        ) : null}
      </div>

      {result && result.sample.length > 0 ? (
        <div className="cp-scope-preview__sample">
          {result.sample.map((node) => (
            <QueryBadge
              key={node.node_hash}
              label={`${node.display_tag || node.node_hash.slice(0, 12)}${node.region ? ` · ${node.region.toUpperCase()}` : ""}${node.protocol ? ` · ${protocolLabels[node.protocol] ?? node.protocol}` : ""}`}
            />
          ))}
          {result.matched > result.sample.length ? (
            <span>{t("另有 {{count}} 个未显示", { count: result.matched - result.sample.length })}</span>
          ) : null}
        </div>
      ) : null}

      {result && result.matched === 0 ? (
        <p className="cp-form-warning">{t("当前条件没有匹配到任何节点；请减少条件或放宽取值。")}</p>
      ) : null}

      {exclusions.length > 0 ? (
        <p className="cp-scope-preview__exclusions">
          {t("被排除的原因（首个命中的条件）：")}{" "}
          {exclusions
            .map(([reason, count]) => {
              const label = reason.startsWith(qualityReasonPrefix)
                ? exclusionLabels.QUALITY_REJECTED
                : exclusionLabels[reason] ?? reason;
              return `${t(label)} ${count}`;
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
  const values: PlatformFormValues = useMemo(
    () => ({ ...defaultPlatformFormValues, ...watched }) as PlatformFormValues,
    [watched],
  );
  const criteriaKey = useMemo(() => JSON.stringify(toPlatformCriteria(values)), [values]);
  const facets: PlatformNodeFacets | undefined = facetsQuery.data;
  const regionValues = listValues(values.region_filters_text);
  const positiveRegions = regionValues.filter((value) => !value.startsWith("!"));

  const regionOptions = mergeOptions(
    (facets?.regions ?? []).map((code) => ({ value: code, label: code.toUpperCase() })),
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
    <div className="cp-criteria">
      <div className="cp-criteria__intro">
        <div className="cp-criteria__title">
          <span>{t("平台加载的节点")}</span>
          <QueryBadge tone="signal" label={t("条件同时生效（AND）")} />
        </div>
        <p>{t("节点必须同时满足下列每一项条件；同一项里勾选多个值时，命中其中任意一个即通过（OR）。某项留空表示不做限制。")}</p>
        {facets ? (
          <span>
            {facets.truncated
              ? t("选项来自节点池（已扫描 {{scanned}} / {{total}} 个节点）", {
                  scanned: facets.scanned,
                  total: facets.total_nodes,
                })
              : t("选项来自当前节点池（共 {{total}} 个节点）", { total: facets.total_nodes })}
          </span>
        ) : facetsQuery.isError ? (
          <span className="cp-form-error">{t("节点池选项暂时不可用，仍可保存已有条件。")}</span>
        ) : null}
      </div>

      <div className="cp-criteria__grid">
        <CriterionField label={t("国家 / 地区")} hint={t("取自节点的出口地区（GeoIP 或探测结果）。")}>
          <CheckboxList
            idPrefix={idPrefix}
            name="region"
            options={regionOptions}
            selected={positiveRegions}
            onChange={(next) => {
              const excluded = regionValues.filter((value) => value.startsWith("!"));
              form.setValue("region_filters_text", [...next, ...excluded].join("\n"), { shouldDirty: true });
            }}
          />
        </CriterionField>

        <CriterionField label={t("网络类型（ip_type）")} hint={t("取值来自质量评估模型。")}>
          <CheckboxList
            idPrefix={idPrefix}
            name="ip_type"
            options={ipTypeOptions.map((value) => ({
              value,
              label: `${t(ipTypeLabels[value] ?? value)} (${value})`,
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
              label: `${t(purityBandLabels[value] ?? value)} (${value})`,
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

        <CriterionField label={t("订阅")} hint={t("节点必须仍被所选订阅引用。")}>
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
        <div className="cp-criteria__legacy">
          <span>{t("保留的地区排除项（旧配置）：")}</span>
          {regionValues
            .filter((value) => value.startsWith("!"))
            .map((value) => (
              <ControlButton
                key={value}
                type="button"
                variant="ghost"
                size="sm"
                onPress={() =>
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
              </ControlButton>
            ))}
        </div>
      ) : null}

      <details className="cp-criteria__advanced">
        <summary>
          <ChevronDown size={14} aria-hidden />
          {t("高级：解锁要求（可选）")}
        </summary>
        <div className="cp-criteria__advanced-body">
          <UnlockRequirementsField form={form} />
        </div>
      </details>
    </div>
  );
}

function UnlockRequirementsField({ form }: { form: UseFormReturn<PlatformFormValues> }) {
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
    <div className="cp-unlock-requirements">
      <div>
        <strong>{t("解锁要求")}</strong>
        <p>{t("留空表示不要求任何解锁结果。勾选后，缺少对应检测结果的节点不会进入该平台。")}</p>
      </div>
      {entries.length === 0 ? (
        <p className="cp-muted">{t("未设置解锁要求。")}</p>
      ) : (
        <div className="cp-unlock-requirements__list">
          {entries.map((entry) => {
            const rule = checks.find((check) => check.id === entry.checkID);
            const label = rule?.name && rule.name !== rule.id ? `${rule.name} (${entry.checkID})` : entry.checkID;
            return (
              <div key={entry.checkID} className="cp-unlock-requirement">
                <span title={entry.checkID}>{label}</span>
                <ControlSelect
                  aria-label={t("要求的结果")}
                  value={entry.outcome}
                  onChange={(event) =>
                    write(
                      entries.map((item) =>
                        item.checkID === entry.checkID ? { ...item, outcome: event.target.value } : item,
                      ),
                    )
                  }
                >
                  <option value="">{t("任意结果")}</option>
                  {INTEL_CHECK_OUTCOMES.map((outcome) => (
                    <option key={outcome} value={outcome}>{outcome}</option>
                  ))}
                </ControlSelect>
                <ControlButton
                  type="button"
                  variant="ghost"
                  size="sm"
                  isIconOnly
                  aria-label={t("移除")}
                  onPress={() => write(entries.filter((item) => item.checkID !== entry.checkID))}
                >
                  <X size={12} aria-hidden />
                </ControlButton>
              </div>
            );
          })}
        </div>
      )}
      <ControlSelect
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
      </ControlSelect>
      {checksQuery.isError ? <p className="cp-form-error">{t("检测项读取失败，稍后可重试。")}</p> : null}
    </div>
  );
}
