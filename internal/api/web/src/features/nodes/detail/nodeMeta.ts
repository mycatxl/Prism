import type { BadgeProps } from "../../../components/ui/Badge";
import { formatGoDuration } from "../../../lib/time";
import type { NodeSummary } from "../types";

/**
 * Facts about one node that more than one surface needs: the list row, the
 * drawer and now the detail page all name a node the same way and grade its
 * state with the same rules. Kept here so the wording cannot drift between the
 * three of them.
 */

/** The badge tones the node surfaces use. */
export type NodeTone = NonNullable<BadgeProps["tone"]>;

/**
 * The state of a node, in the order the rules apply — one verdict in four words,
 * the same words the pool and the drawer use: a disabled node was turned off by
 * an operator (neutral); a node without a usable outbound is critical; an open
 * circuit means the node still routes, just not cleanly, so it is degraded; the
 * rest is healthy.
 */
export function nodeStatus(node: NodeSummary): { label: string; tone: NodeTone } {
  if (!node.enabled) return { label: "禁用", tone: "neutral" };
  if (!node.has_outbound) return { label: "危急", tone: "alert" };
  if (node.circuit_open_since) return { label: "降级", tone: "warn" };
  return { label: "健康", tone: "signal" };
}
export function nodeName(node: NodeSummary): string {
  return node.display_tag || node.tags[0]?.tag || node.node_hash.slice(0, 12);
}

/** Protocol ids are lowercase tokens; the labels are the names the vendors use. */
export const PROTOCOL_LABELS: Record<string, string> = {
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

/** The display label of a protocol id, falling back to the raw token. */
export function protocolLabel(protocol: string | undefined): string {
  if (!protocol) return "";
  return PROTOCOL_LABELS[protocol] || protocol;
}

/**
 * parseGoDurationMs reads a Go duration string (`1h30m`, `45m`) into
 * milliseconds, or null when the value is absent or not a duration. It accepts
 * the same grammar `formatGoDuration` formats, so a `sticky_ttl` round-trips.
 */
export function parseGoDurationMs(input: string | undefined | null): number | null {
  const raw = (input ?? "").trim();
  if (!raw) return null;
  const pattern = /(\d+(?:\.\d+)?)(h|m|s)/g;
  let totalMs = 0;
  let consumed = 0;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(raw)) !== null) {
    const value = Number(match[1]);
    if (!Number.isFinite(value)) return null;
    consumed += match[0].length;
    if (match[2] === "h") totalMs += value * 3_600_000;
    else if (match[2] === "m") totalMs += value * 60_000;
    else totalMs += value * 1_000;
  }
  if (!consumed || consumed !== raw.length) return null;
  return totalMs;
}

/**
 * The tone of a lease's remaining life: more than two days is quiet, the last
 * two days warn, and an expired lease alerts. The 48h line is the console-wide
 * rule for "soon".
 */
export const LEASE_WARN_THRESHOLD_MS = 48 * 3_600_000;

export function leaseRemainingTone(remainingMs: number): "signal" | "warn" | "alert" {
  if (remainingMs <= 0) return "alert";
  if (remainingMs <= LEASE_WARN_THRESHOLD_MS) return "warn";
  return "signal";
}

/**
 * The share of the lease's window that is still ahead, 0..100, or null when the
 * window is unknown (no sticky TTL and an unusable access stamp): a bar that
 * guesses at its denominator is a bar that lies.
 */
export function leaseFillPercent(remainingMs: number, windowMs: number | null): number | null {
  if (windowMs === null || windowMs <= 0) return null;
  if (remainingMs <= 0) return 0;
  return Math.max(0, Math.min(100, (remainingMs / windowMs) * 100));
}

/**
 * A future duration in the same voice as `formatGoDuration` (which only speaks
 * of the past): the remaining milliseconds are re-encoded as a Go duration and
 * handed to the existing formatter, so both surfaces say "3 天 2 小时" alike.
 */
export function formatRemainingDuration(remainingMs: number): string {
  const totalSeconds = Math.max(0, Math.floor(remainingMs / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  const parts: string[] = [];
  if (hours > 0) parts.push(`${hours}h`);
  if (minutes > 0) parts.push(`${minutes}m`);
  if (hours === 0 && seconds > 0) parts.push(`${seconds}s`);
  return formatGoDuration(parts.join("") || "0s");
}
