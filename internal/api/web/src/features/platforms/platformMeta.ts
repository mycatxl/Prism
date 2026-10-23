import type { BadgeProps } from "../../components/ui/Badge";
import type { Platform } from "./types";

/**
 * Facts about a platform that more than one surface states the same way: the
 * list row, the detail page and the workbench all grade a platform with the
 * same rule and the same words. Kept here so the wording cannot drift between
 * them, the way `nodeMeta.ts` does it for nodes.
 */

/** The badge tones the platform surfaces use. */
export type PlatformTone = NonNullable<BadgeProps["tone"]>;

/**
 * The state of a platform, one verdict in two words.
 *
 * The platform record carries no health field of its own; the one fact the
 * server always fills on the list and the detail endpoints is
 * `routable_node_count`, and a platform with nothing routable cannot serve a
 * single request — the workbench already flags exactly that as "needs
 * attention". So the dot is derived from the record's own number: anything
 * routable is 正常, nothing is 无可路由节点. There is no unknown state to fake:
 * the number is always present.
 */
export function platformStatus(platform: Platform): { label: string; tone: PlatformTone } {
  if (platform.routable_node_count <= 0) return { label: "无可路由节点", tone: "alert" };
  return { label: "正常", tone: "signal" };
}

/** The `.state-dot` variant for a platform tone; neutral keeps the bare dot. */
export function platformDotClass(tone: PlatformTone): string {
  return tone === "neutral" ? "state-dot" : `state-dot state-dot--${tone}`;
}
