import assert from "node:assert/strict";
import test from "node:test";
import { convertPaymentMicro, parseAmountToMicro, parsePaymentRate, formatPaymentRate } from "../src/lib/money.ts";

test("payment previews match exact ledger floor rounding", () => {
  assert.equal(convertPaymentMicro(10_000, 0.00016), 1);
  assert.equal(convertPaymentMicro(10_000, 0.00006), null);
  assert.equal(convertPaymentMicro(50_000_123, 0.125), 6_250_015);
  assert.equal(convertPaymentMicro(1_000_000_000, 1e-7), 100);
  for (const rate of [NaN, Infinity, -1, 0, 1_000_001]) assert.equal(convertPaymentMicro(1_000_000, rate), null);
  assert.equal(convertPaymentMicro(Number.MAX_SAFE_INTEGER, 1), null);
});

test("decimal input rejects alternate notation, overflow and excessive length", () => {
  for (const raw of ["abc", "-1", "1e6", "0x10", "9".repeat(1000), "0".repeat(1000)+"1", "1.0000001", "9007199254.740992"]) assert.equal(parseAmountToMicro(raw), null, raw);
  assert.equal(parseAmountToMicro("9007199254.740991"), Number.MAX_SAFE_INTEGER);
  assert.equal(parseAmountToMicro("1000000.000001"), 1_000_000_000_001);
  for (const raw of ["abc", "-1", "1e6", "0x10", "9".repeat(1000), "0", "1000000.0000000001", "0.99999999999999999", "0.10000000000000001", "999999.99999999999999"]) assert.equal(parsePaymentRate(raw), null, raw);
  assert.equal(parsePaymentRate("0.1000"), 0.1);
  assert.equal(parsePaymentRate("0.0000001"), 1e-7);
  assert.equal(parsePaymentRate("1000000"), 1_000_000);
  assert.equal(convertPaymentMicro(1_000_000_000_000, 1), 1_000_000_000_000);
  assert.equal(convertPaymentMicro(1_000_000_000_001, 1), null);
});

test("small rates survive settings reload in plain decimal notation", () => {
  for (const rate of [1e-7, 1.2345e-12, 0.125, 1, 1000000]) {
    assert(!formatPaymentRate(rate).includes("e"));
    assert.equal(parsePaymentRate(formatPaymentRate(rate)), rate);
  }
});
