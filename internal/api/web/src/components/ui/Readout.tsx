import { useEffect, useRef, useState, type ReactNode } from "react";
import { cn } from "../../lib/cn";

/**
 * The KPI anatomy, taken from Grafana's BigValue and go-view's animated counter:
 *
 *   - the value is 2.5x the unit and 2.5x the label, because that ratio is what
 *     lets a number be read from across a room without the label disappearing;
 *   - the numerals are mono and tabular, so a value that changes every few seconds
 *     does not shift its own layout;
 *   - a value that changed rolls to the new value instead of jumping, so the
 *     movement itself says "this is live" without a pulsing dot.
 *
 * The earlier panel had the opposite shape: four 28px tiles with a trend glyph
 * each, which spends a whole band on numbers nobody reads twice.
 */

const COUNT_MS = 800;

/** Reads a leading number out of a formatted string, so "1,204" can roll. */
function parseLeadingNumber(
  value: string,
): { prefix: string; number: number; suffix: string; decimals: number } | null {
  const match = value.match(/^([^\d-]*)(-?[\d,]+(?:\.\d+)?)(.*)$/);
  if (!match) {
    return null;
  }
  const digits = match[2].replace(/,/g, "");
  const number = Number(digits);
  if (!Number.isFinite(number)) {
    return null;
  }
  const dot = digits.indexOf(".");
  return {
    prefix: match[1],
    number,
    suffix: match[3],
    decimals: dot >= 0 ? digits.length - dot - 1 : 0,
  };
}

function formatNumber(value: number, decimals: number, grouped: boolean): string {
  const fixed = value.toFixed(decimals);
  if (!grouped) {
    return fixed;
  }
  const [whole, fraction] = fixed.split(".");
  const withSeparators = whole.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  return fraction ? withSeparators + "." + fraction : withSeparators;
}

/**
 * A numeral that rolls to its new value. The first render is authoritative and
 * never animates: the panel loads into a task, and an entrance animation on a
 * number the operator is about to read is a delay, not a signal.
 *
 * Every state write goes through a frame. That is not ceremony: a synchronous write
 * inside the effect would re-render, re-run the effect, and leave the value racing
 * its own animation on the first poll.
 */
export function Numeral({
  value,
  className,
  animate = true,
}: {
  value: string;
  className?: string;
  animate?: boolean;
}) {
  const reduced =
    typeof window !== "undefined" &&
    window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
  const [shown, setShown] = useState(value);
  const previous = useRef(value);

  useEffect(() => {
    const from = previous.current;
    previous.current = value;

    const parsedFrom = parseLeadingNumber(from);
    const parsedTo = parseLeadingNumber(value);
    const canRoll =
      animate &&
      !reduced &&
      from !== value &&
      parsedFrom !== null &&
      parsedTo !== null &&
      // A prefix or suffix that changed is not a roll, it is a different unit: swap.
      parsedFrom.prefix === parsedTo.prefix &&
      parsedFrom.suffix === parsedTo.suffix;

    if (!canRoll) {
      const swap = requestAnimationFrame(() => setShown(value));
      return () => cancelAnimationFrame(swap);
    }
    // Narrowed for the type checker, which cannot see through the guard above.
    if (parsedFrom === null || parsedTo === null) {
      return;
    }

    const grouped = parsedTo.number >= 1000 || from.includes(",");
    const started = performance.now();
    let frame = 0;
    const tick = (now: number) => {
      const t = Math.min(1, (now - started) / COUNT_MS);
      // easeOutCubic: quick to the new value, settles without overshoot.
      const eased = 1 - Math.pow(1 - t, 3);
      const current = parsedFrom.number + (parsedTo.number - parsedFrom.number) * eased;
      setShown(
        parsedTo.prefix + formatNumber(current, parsedTo.decimals, grouped) + parsedTo.suffix,
      );
      if (t < 1) {
        frame = requestAnimationFrame(tick);
      } else {
        setShown(value);
      }
    };
    frame = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frame);
  }, [value, animate, reduced]);

  return <span className={cn("numeral", className)}>{shown}</span>;
}

/**
 * One measured value: number, unit, label, and an optional detail line that carries
 * the fact's own qualifier (a share of a total, a window name).
 */
export function Readout({
  value,
  unit,
  label,
  hint,
  tone = "ink",
  size = "md",
  className,
  animate = true,
}: {
  value: ReactNode;
  unit?: ReactNode;
  label?: ReactNode;
  hint?: ReactNode;
  tone?: "ink" | "signal" | "live" | "warn" | "alert" | "muted";
  size?: "sm" | "md" | "lg" | "xl";
  className?: string;
  animate?: boolean;
}) {
  const toneClass = {
    ink: "text-ink",
    signal: "text-signal",
    live: "text-live",
    warn: "text-warn",
    alert: "text-alert",
    muted: "text-ink-faint",
  }[tone];

  // 2.5 : 1 between the number and its unit, and between the number and the label.
  const sizeClass = {
    sm: "text-base",
    md: "text-xl",
    lg: "text-2xl",
    xl: "text-4xl",
  }[size];

  return (
    <div className={cn("min-w-0", className)}>
      {label && <div className="micro truncate">{label}</div>}
      <div className="mt-1 flex items-baseline gap-1.5">
        {typeof value === "string" || typeof value === "number" ? (
          <Numeral value={String(value)} animate={animate} className={cn(sizeClass, toneClass)} />
        ) : (
          <span className={cn("numeral", sizeClass, toneClass)}>{value}</span>
        )}
        {unit && <span className="label readout">{unit}</span>}
      </div>
      {hint && <div className="mt-1 truncate text-xs text-ink-faint">{hint}</div>}
    </div>
  );
}

/**
 * Several readouts on one baseline, separated by hairlines rather than boxed into
 * cards. A strip of instrument values, not a row of KPI tiles: tiles only become
 * wrong when the band is nothing but tiles.
 */
export function ReadoutStrip({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className={cn("panel flex flex-wrap divide-x divide-rule overflow-hidden", className)}>
      {children}
    </div>
  );
}

export function ReadoutCell({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("min-w-[9.5rem] flex-1 px-4 py-3", className)}>{children}</div>;
}
