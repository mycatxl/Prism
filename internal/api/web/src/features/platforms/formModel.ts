import { z } from "zod";
import { allocationPolicies, emptyAccountBehaviors, missActions } from "./constants";
import { parseHeaderLines, parseLinesToList } from "./formParsers";
import type { Platform, PlatformCreateInput, PlatformScopeSpec, PlatformUpdateInput } from "./types";

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
  regex_filters_text: z.string().optional(),
  // The region criterion stays line-based so that a legacy platform's negated
  // entries ("!hk") survive an edit: the picker only toggles positive lines.
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
  regex_filters_text: "",
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
};

export function platformToFormValues(platform: Platform): PlatformFormValues {
  const regexFilters = Array.isArray(platform.regex_filters) ? platform.regex_filters : [];
  const regionFilters = Array.isArray(platform.region_filters) ? platform.region_filters : [];

  return {
    name: platform.name,
    sticky_ttl: platform.sticky_ttl,
    regex_filters_text: regexFilters.join("\n"),
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
  };
}

/** listValues reads the values of one newline-separated form field. */
export function listValues(text: string | undefined): string[] {
  return parseLinesToList(text);
}

/**
 * toggleListValue adds or removes one value of a newline-separated form field and
 * returns the new text.
 *
 * It is how the multi-select pickers write into the line-based fields: every
 * other line (an entry the operator cannot see in the picker, such as a legacy
 * "!hk" exclusion) is preserved untouched.
 */
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
 * toPlatformCriteria is the single source of the criteria payload: the create /
 * update body and the live preview spec are built from this one function, so the
 * preview can never describe criteria the saved platform would not apply.
 *
 * Every criterion is ANDed with the others and the values inside one criterion
 * are alternatives; an empty list means "do not restrict on this criterion".
 */
export function toPlatformCriteria(values: PlatformFormValues): Required<PlatformScopeSpec> {
  return {
    regex_filters: listValues(values.regex_filters_text),
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

export function toPlatformCreateInput(values: PlatformFormValues): PlatformCreateInput {
  return {
    ...toPlatformPayloadBase(values),
    sticky_ttl: values.sticky_ttl?.trim() || undefined,
  };
}

export function toPlatformUpdateInput(values: PlatformFormValues): PlatformUpdateInput {
  return {
    ...toPlatformPayloadBase(values),
    sticky_ttl: values.sticky_ttl?.trim() || "",
  };
}
