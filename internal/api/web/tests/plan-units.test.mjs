import assert from "node:assert/strict";
import test from "node:test";
import {
  BITS_PER_MBPS,
  BYTES_PER_GB,
  exactUnitsText,
  unitsToBase,
} from "../src/features/admin/plan-units.ts";

/*
 * The plan form keeps the stored byte/bit count and only shows it as GB/Mbps, so
 * that editing one field cannot rewrite another. "Load → save" therefore has to
 * be the identity, for every quota, including the fractional ones an operator
 * actually types.
 *
 * `1.5 GiB` is the case an audit found: `Math.round(1.5 GiB / 1 GiB)` rendered as
 * "2", and saving a name change multiplied that back into 2 GiB.
 */

const QUOTAS = [
  1.5 * BYTES_PER_GB,
  0.25 * BYTES_PER_GB,
  1 * BYTES_PER_GB,
  2 * BYTES_PER_GB,
  100 * BYTES_PER_GB,
  1500 * BYTES_PER_GB,
  BYTES_PER_GB + 1,
  123456789,
  0,
];

test("a quota survives load → display → save unchanged", () => {
  for (const bytes of QUOTAS) {
    const shown = exactUnitsText(bytes, BYTES_PER_GB);
    assert.equal(
      unitsToBase(shown, BYTES_PER_GB),
      bytes,
      `${bytes} bytes rendered as "${shown}" and came back as ${unitsToBase(shown, BYTES_PER_GB)}`,
    );
  }
});

test("1.5 GiB is shown as 1.5, not 2", () => {
  const text = exactUnitsText(1.5 * BYTES_PER_GB, BYTES_PER_GB);
  assert.equal(text, "1.5");
  assert.equal(unitsToBase(text, BYTES_PER_GB), 1610612736);
  assert.equal(unitsToBase(text, BYTES_PER_GB) / BYTES_PER_GB, 1.5);
});

test("bandwidth survives the same round trip", () => {
  for (const bps of [1.5 * BITS_PER_MBPS, 100 * BITS_PER_MBPS, 10_000_000, 0]) {
    const shown = exactUnitsText(bps, BITS_PER_MBPS);
    assert.equal(unitsToBase(shown, BITS_PER_MBPS), bps, `${bps} bps rendered as "${shown}"`);
  }
  assert.equal(exactUnitsText(1.5 * BITS_PER_MBPS, BITS_PER_MBPS), "1.5");
});

test("blank or junk input means unlimited, not zero bytes", () => {
  for (const text of ["", "   ", "abc", "0", "-5"]) {
    assert.equal(unitsToBase(text, BYTES_PER_GB), 0, `"${text}"`);
  }
  assert.equal(exactUnitsText(0, BYTES_PER_GB), "0");
  assert.equal(exactUnitsText(Number.NaN, BYTES_PER_GB), "0");
});
