import { useEffect, useRef, useState, type ReactNode } from "react";
import { cn } from "../../lib/cn";

/**
 * KPI readout voice:
 *   - numeral at `--text-2xl`/`--text-3xl` weight 600 tabular mono;
 *   - label in micro-caps (`--color-ink-faint`);
 *   - optional delta chip slot (`delta`) and qualifier (`hint`).
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
 * A numeral that rolls to its new value when updated, respecting reduced motion.
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
      parsedFrom.prefix === parsedTo.prefix &&
      parsedFrom.suffix === parsedTo.suffix;

    if (!canRoll) {
      const swap = requestAnimationFrame(() => setShown(value));
      return () => cancelAnimationFrame(swap);
    }
    if (parsedFrom === null || parsedTo === null) {
      return;
    }

    const grouped = parsedTo.number >= 1000 || from.includes(",");
    const started = performance.now();
    let frame = 0;
    const tick = (now: number) => {
      const t = Math.min(1, (now - started) / COUNT_MS);
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
 * One measured KPI value: number, unit, micro-caps label, optional delta chip slot,
 * and optional qualifier hint.
 */
export function Readout({
  value,
  unit,
  label,
  hint,
  delta,
  tone = "ink",
  size = "md",
  className,
  animate = true,
}: {
  value: ReactNode;
  unit?: ReactNode;
  label?: ReactNode;
  hint?: ReactNode;
  delta?: ReactNode;
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

  const sizeClass = {
    sm: "text-lg",
    md: "text-2xl",
    lg: "text-3xl",
    xl: "text-4xl",
  }[size];

  return (
    <div className={cn("min-w-0", className)}>
      {label && <div className="micro truncate">{label}</div>}
      <div className="mt-1.5 flex items-baseline gap-1.5">
        {typeof value === "string" || typeof value === "number" ? (
          <Numeral value={String(value)} animate={animate} className={cn(sizeClass, toneClass)} />
        ) : (
          <span className={cn("numeral", sizeClass, toneClass)}>{value}</span>
        )}
        {unit && <span className="label readout">{unit}</span>}
        {delta && <span className="ml-auto shrink-0 text-right">{delta}</span>}
      </div>
      {hint && <div className="mt-1 truncate text-xs text-ink-faint">{hint}</div>}
    </div>
  );
}

/**
 * Several readouts on one glass strip, separated by soft hairlines.
 */
export function ReadoutStrip({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className={cn("panel flex flex-wrap divide-x divide-rule-faint overflow-hidden", className)}>
      {children}
    </div>
    );
  }
export function ReadoutCell({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("min-w-[9.5rem] flex-1 px-4 py-3.5", className)}>{children}</div>;
}
