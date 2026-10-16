/**
 * The two unit conversions the plan form needs, kept out of the component so the
 * gate tests can exercise them directly.
 *
 * A plan stores `traffic_bytes` and `max_bandwidth_bps`; the form asks in GB and
 * Mbps groups. Rounding on load (`Math.round(bytes / GB)`) and multiplying back
 * on save rewrote plans nobody edited: a 1.5 GiB quota came back as 2 GiB from a
 * rename that only touched the name. So the text a field shows is the shortest
 * decimal that still converts back to the stored value exactly, and the form
 * keeps the stored value itself until the operator edits that field — the
 * operator's own number is the only thing ever re-converted.
 */

export const BYTES_PER_GB = 1024 * 1024 * 1024;
export const BITS_PER_MBPS = 1_000_000;

/**
 * The shortest decimal in `perUnit` units that converts back to exactly `value`.
 * `0` and anything unusable is the form's own "unlimited" spelling.
 */
export function exactUnitsText(value: number, perUnit: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0";
  const units = value / perUnit;
  for (let digits = 0; digits <= 6; digits += 1) {
    const text = units.toFixed(digits);
    if (Math.round(Number(text) * perUnit) === value) {
      return digits === 0 ? text : text.replace(/0+$/, "").replace(/\.$/, "");
    }
  }
  // A value no short decimal can express (a byte count that is not a round
  // fraction of the unit) keeps its full precision: it is only ever displayed,
  // and the form still submits the stored number.
  return String(units);
}

/**
 * `text` read as a count of `perUnit` units, as a whole number of base units.
 * Blank, unparsable and non-positive input all mean "unlimited" (0).
 */
export function unitsToBase(text: string, perUnit: number): number {
  const units = Number(text.trim());
  if (!Number.isFinite(units) || units <= 0) return 0;
  return Math.round(units * perUnit);
}
