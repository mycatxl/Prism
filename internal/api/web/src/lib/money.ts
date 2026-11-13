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
 * formatMicroCompact renders an amount without trailing zeros, for an input the
 * operator edits. The stored value keeps six decimals; this is only what the
 * field shows, so "10.000000" does not come back as a number nobody typed.
 */
export function formatMicroCompact(micro: number): string {
  return formatMicro(micro).replace(/\.?0+$/, "");
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
  if (trimmed === "" || trimmed.length > 32) return null;
  if (!/^\d+(\.\d{1,6})?$/.test(trimmed)) return null;

  const [whole, fraction = ""] = trimmed.split(".");
  const wholeMicro = BigInt(whole) * BigInt(MICRO_PER_UNIT);
  const fractionMicro = BigInt(fraction.padEnd(6, "0"));
  const total = wholeMicro + fractionMicro;
  if (total > BigInt(Number.MAX_SAFE_INTEGER)) return null;
  return Number(total);
}

/** Form rates use plain decimal notation and retain the exact range check. */
export function parsePaymentRate(input: string): number | null {
  const raw = input.trim();
  if (raw.length > 32 || !/^\d+(\.\d+)?$/.test(raw)) return null;
  const [whole, fraction = ""] = raw.split(".");
  const numerator = BigInt(whole + fraction);
  if (numerator <= 0n || numerator > 1_000_000n * 10n ** BigInt(fraction.length)) return null;
  const rate = Number(raw);
  const [storedWhole, storedFraction = ""] = formatPaymentRate(rate).split(".");
  const storedNumerator = BigInt(storedWhole + storedFraction);
  if (storedNumerator * 10n ** BigInt(fraction.length) !== numerator * 10n ** BigInt(storedFraction.length)) return null;
  return rate;
}

/** Expand numeric rates for editable forms, which reject exponent notation. */
export function formatPaymentRate(rate: number): string {
  const [coefficient, exponentText] = String(rate).split("e-");
  if (exponentText === undefined) return coefficient;
  const [whole, fraction = ""] = coefficient.split(".");
  return `0.${"0".repeat(Number(exponentText) - whole.length)}${whole}${fraction}`;
}

/** Match the ledger: exact decimal multiplication, rounded down to micro units. */
export function convertPaymentMicro(amountMicro: number, rate: number): number | null {
  if (!Number.isSafeInteger(amountMicro) || amountMicro <= 0 || !Number.isFinite(rate) || rate <= 0 || rate > 1_000_000) return null;
  const [coefficient, exponentText = "0"] = String(rate).toLowerCase().split("e");
  const [whole, fraction = ""] = coefficient.split(".");
  const exponent = Number(exponentText) - fraction.length;
  const numerator = BigInt(amountMicro) * BigInt(whole + fraction);
  const credit = exponent >= 0 ? numerator * 10n ** BigInt(exponent) : numerator / 10n ** BigInt(-exponent);
  return credit > 0n && credit <= 1_000_000_000_000n ? Number(credit) : null;
}
