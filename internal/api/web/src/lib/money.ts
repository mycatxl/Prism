/**
 * Money is an integer count of micro-units: one micro is 1e-6 USDT, which is the
 * token's own precision. Everything that crosses the wire is that integer, and
 * this is the only place it becomes text.
 *
 * The division happens once, here, and the result is rendered with exactly six
 * decimals - never with a float that has been rounded twice. A balance of
 * 10_000_000 is "10.000000", and 10_000_001 is "10.000001": the trailing digit
 * is the whole point of the scheme, so it must survive the formatter.
 */

const MICRO_PER_UNIT = 1_000_000;

/** formatMicro renders a micro-unit amount as a fixed six-decimal string. */
export function formatMicro(micro: number): string {
  if (!Number.isFinite(micro)) return "0.000000";
  const negative = micro < 0;
  const absolute = Math.abs(Math.trunc(micro));
  const whole = Math.floor(absolute / MICRO_PER_UNIT);
  const fraction = absolute % MICRO_PER_UNIT;
  const digits = String(fraction).padStart(6, "0");
  return `${negative ? "-" : ""}${whole}.${digits}`;
}

/** formatMicroWithUnit renders an amount with the currency it is denominated in. */
export function formatMicroWithUnit(micro: number): string {
  return `${formatMicro(micro)} USDT`;
}

/**
 * parseAmountToMicro turns what a person typed into micro-units, or null when it
 * is not an amount.
 *
 * Accepts at most six decimals, because that is what the unit can represent: a
 * seventh decimal would either be silently dropped or rounded, and either one
 * changes how much money somebody is moving.
 */
export function parseAmountToMicro(input: string): number | null {
  const trimmed = input.trim();
  if (trimmed === "") return null;
  if (!/^\d+(\.\d{1,6})?$/.test(trimmed)) return null;

  const [whole, fraction = ""] = trimmed.split(".");
  const wholeMicro = Number(whole) * MICRO_PER_UNIT;
  const fractionMicro = Number(fraction.padEnd(6, "0"));
  const total = wholeMicro + fractionMicro;
  if (!Number.isSafeInteger(total)) return null;
  return total;
}
