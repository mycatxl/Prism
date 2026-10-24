import { z } from "zod";
import { allocationPolicies, emptyAccountBehaviors, missActions } from "./constants";
import { parseHeaderLines, parseLinesToList } from "./formParsers";
import type {
  Platform,
  PlatformCreateInput,
  PlatformQualityPolicy,
  PlatformScopeSpec,
  PlatformUpdateInput,
} from "./types";

const platformNameForbiddenChars = ".:|/\\@?#%~";
const platformNameForbiddenSpacing = " \t\r\n";
const platformNameReserved = "api";

function containsAny(source: string, chars: string): boolean {
  for (const ch of chars) {
    if (source.includes(ch)) {
      return true;
    }
  }
  return false;
}

export const platformNameRuleHint = "平台名不能包含 .:|/\\@?#%~、空格、Tab、换行、回车，也不能为保留字。";

export const platformFormSchema = z.object({
  name: z.string().trim()
    .min(1, "平台名称不能为空")
    .refine((value) => !containsAny(value, platformNameForbiddenChars), {
      message: "平台名称不能包含字符 .:|/\\@?#%~",
    })
    .refine((value) => !containsAny(value, platformNameForbiddenSpacing), {
      message: "平台名称不能包含空格、Tab、换行、回车",
    })
    .refine((value) => value.toLowerCase() !== platformNameReserved, {
      message: "平台名称不能为保留字",
    }),
  sticky_ttl: z.string().optional(),
  region_filters_text: z.string().optional(),
  ip_types: z.array(z.string()),
  purity_bands: z.array(z.string()),
  subscription_filters: z.array(z.string()),
  protocols: z.array(z.string()),
  reverse_proxy_miss_action: z.enum(missActions),
  reverse_proxy_empty_account_behavior: z.enum(emptyAccountBehaviors),
  reverse_proxy_fixed_account_header: z.string().optional(),
  allocation_policy: z.enum(allocationPolicies),
  passive_circuit_breaker_disabled: z.boolean(),
  // Unlock requirements stay line-based so unknown entries survive an edit.
  required_checks_text: z.string().optional(),
}).superRefine((value, ctx) => {
  if (
    value.reverse_proxy_empty_account_behavior === "FIXED_HEADER" &&
    parseHeaderLines(value.reverse_proxy_fixed_account_header).length === 0
  ) {
    ctx.addIssue({
      code: "custom",
      path: ["reverse_proxy_fixed_account_header"],
      message: "用于提取 Account 的 Headers 不能为空",
    });
  }
});

export type PlatformFormValues = z.infer<typeof platformFormSchema>;

export const defaultPlatformFormValues: PlatformFormValues = {
  name: "",
  sticky_ttl: "",
  region_filters_text: "",
  ip_types: [],
  purity_bands: [],
  subscription_filters: [],
  protocols: [],
  reverse_proxy_miss_action: "TREAT_AS_EMPTY",
  reverse_proxy_empty_account_behavior: "RANDOM",
  reverse_proxy_fixed_account_header: "Authorization",
  allocation_policy: "BALANCED",
  passive_circuit_breaker_disabled: false,
  required_checks_text: "",
};

export function platformToFormValues(platform: Platform): PlatformFormValues {
  const regionFilters = Array.isArray(platform.region_filters) ? platform.region_filters : [];

  return {
    name: platform.name,
    sticky_ttl: platform.sticky_ttl,
    region_filters_text: regionFilters.join("\n"),
    ip_types: [...(platform.ip_types ?? [])],
    purity_bands: [...(platform.purity_bands ?? [])],
    subscription_filters: [...(platform.subscription_filters ?? [])],
    protocols: [...(platform.protocols ?? [])],
    reverse_proxy_miss_action: platform.reverse_proxy_miss_action,
    reverse_proxy_empty_account_behavior: platform.reverse_proxy_empty_account_behavior,
    reverse_proxy_fixed_account_header: platform.reverse_proxy_fixed_account_header,
    allocation_policy: platform.allocation_policy,
    passive_circuit_breaker_disabled: platform.passive_circuit_breaker_disabled,
    required_checks_text: requiredChecksToText(platform.quality_policy?.required_checks),
  };
}

export function requiredChecksToText(required: Record<string, string> | undefined | null): string {
  if (!required || typeof required !== "object") {
    return "";
  }
  return Object.keys(required)
    .sort()
    .map((checkID) => `${checkID}:${required[checkID] ?? ""}`)
    .join("\n");
}

export function textToRequiredChecks(text: string | undefined): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of listValues(text)) {
    const separator = line.indexOf(":");
    const checkID = (separator < 0 ? line : line.slice(0, separator)).trim();
    const outcome = separator < 0 ? "" : line.slice(separator + 1).trim();
    if (checkID) {
      out[checkID] = outcome;
    }
  }
  return out;
}

export function listValues(text: string | undefined): string[] {
  return parseLinesToList(text);
}

export function toggleListValue(
  text: string | undefined,
  value: string,
  normalize?: (value: string) => string,
): string {
  const normalized = normalize ? normalize(value) : value;
  const lines = listValues(text);
  const index = lines.indexOf(normalized);
  if (index >= 0) {
    lines.splice(index, 1);
  } else {
    lines.push(normalized);
  }
  return lines.join("\n");
}

/**
 * The live preview and saved platform payload share this exact criteria shape.
 * Every list is ANDed by the backend; values in one list are alternatives.
 */
export function toPlatformCriteria(values: PlatformFormValues): Required<PlatformScopeSpec> {
  return {
    region_filters: listValues(values.region_filters_text).map((value) => value.toLowerCase()),
    ip_types: [...values.ip_types],
    purity_bands: [...values.purity_bands],
    subscription_filters: [...values.subscription_filters],
    protocols: [...values.protocols],
  };
}

function toPlatformPayloadBase(values: PlatformFormValues) {
  return {
    name: values.name.trim(),
    ...toPlatformCriteria(values),
    reverse_proxy_miss_action: values.reverse_proxy_miss_action,
    reverse_proxy_empty_account_behavior: values.reverse_proxy_empty_account_behavior,
    reverse_proxy_fixed_account_header: parseHeaderLines(values.reverse_proxy_fixed_account_header).join("\n"),
    allocation_policy: values.allocation_policy,
    passive_circuit_breaker_disabled: values.passive_circuit_breaker_disabled,
  };
}

/**
 * PATCH replaces the whole quality_policy object, so fields not edited by this
 * form are copied from the loaded platform instead of being silently cleared.
 */
export function toQualityPolicy(
  values: PlatformFormValues,
  current: PlatformQualityPolicy | undefined,
): PlatformQualityPolicy | undefined {
  const required = textToRequiredChecks(values.required_checks_text);
  const policy: PlatformQualityPolicy = { ...(current ?? {}) };
  if (Object.keys(required).length === 0) {
    delete policy.required_checks;
  } else {
    policy.required_checks = required;
  }
  return Object.keys(policy).length > 0 ? policy : undefined;
}

export function toPlatformCreateInput(
  values: PlatformFormValues,
  currentPolicy?: PlatformQualityPolicy,
): PlatformCreateInput {
  return {
    ...toPlatformPayloadBase(values),
    sticky_ttl: values.sticky_ttl?.trim() || undefined,
    quality_policy: toQualityPolicy(values, currentPolicy),
  };
}

export function toPlatformUpdateInput(
  values: PlatformFormValues,
  currentPolicy?: PlatformQualityPolicy,
): PlatformUpdateInput {
  return {
    ...toPlatformPayloadBase(values),
    sticky_ttl: values.sticky_ttl?.trim() || "",
    quality_policy: toQualityPolicy(values, currentPolicy),
  };
}
