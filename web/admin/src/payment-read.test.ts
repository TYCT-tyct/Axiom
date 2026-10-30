import { expect, it } from "vitest";
import { isProblem } from "./change";
import { formatPercent, parseBufferPercent, parsePercent, readMerchant, readPayments } from "./payment-read";

const paymentWords = { amount: "amount", range: "range", buffer: "buffer" };
const merchantWords = {
  name: "name",
  number: "number",
  settings: "settings",
  secret: "secret",
  amount: "amount",
  fee: "fee",
  range: "range",
  methods: "methods",
};

const merchantValues = {
  kind: "easypay",
  name: " Gateway ",
  enabled: "true",
  currency: "USD",
  ttl_seconds: "1800",
  sort: "1",
  settings: '{"gateway":"https://pay.example.com","pid":"1001","mode":"redirect"}',
  key: "secret-key",
  "method-0-method": "alipay",
  "method-0-upstream": "alipay",
  "method-0-percent": "3",
  "method-0-fixed": "0",
  "method-0-enabled": "true",
};

it("reads a percentage as millionths with integer arithmetic", () => {
  expect(parsePercent("3", 500_000)).toBe(30_000);
  expect(parsePercent("0.005", 100_000)).toBe(50);
  expect(parsePercent("0.0001", 100_000)).toBe(1);
  expect(parsePercent("10", 100_000)).toBe(100_000);
  for (const text of ["", "10.0001", "1.23456", "-1", "01", "1e2"]) {
    expect(parseBufferPercent(text), text).toBeNull();
  }
});

it("writes a percentage without a floating point", () => {
  expect(formatPercent(30_000)).toBe("3");
  expect(formatPercent(35_000)).toBe("3.5");
  expect(formatPercent(50)).toBe("0.005");
  expect(formatPercent(0)).toBe("0");
});

it("reads the payment settings with exact amounts and the buffer", () => {
  expect(
    readPayments(
      { open: "true", credits_min: "1.5", credits_max: "20", top_up_buffer_ppm: "3.5" },
      "USD",
      true,
      paymentWords,
    ),
  ).toEqual({
    open: true,
    credits_min: { amount: "1500000000", currency: "USD" },
    credits_max: { amount: "20000000000", currency: "USD" },
    top_up_buffer_ppm: 35_000,
  });
  expect(readPayments({ open: "true", credits_min: "2", credits_max: "1" }, "USD", true, paymentWords)).toEqual({
    problem: "range",
  });
  expect(readPayments({ open: "true", credits_min: "1.0000000001" }, "USD", true, paymentWords)).toEqual({
    problem: "amount",
  });
  // An older node has no buffer field: the dialog leaves it out instead of sending a value it cannot read back.
  expect(readPayments({ open: "false" }, "USD", false, paymentWords)).toEqual({ open: false });
});

it("reads a new merchant with its methods and sealed key", () => {
  const read = readMerchant(merchantValues, undefined, merchantWords);
  expect(isProblem(read) ? read : null).toBeNull();
  if (isProblem(read)) {
    return;
  }
  expect(read).toEqual({
    kind: "easypay",
    name: "Gateway",
    enabled: true,
    currency: "USD",
    ttl_seconds: 1800,
    refund_enabled: false,
    user_refund: false,
    sort: 1,
    settings: { gateway: "https://pay.example.com", pid: "1001", mode: "redirect" },
    secrets: { key: "secret-key" },
    methods: [
      {
        method: "alipay",
        upstream: "alipay",
        fee_ppm: 30_000,
        fee_fixed: { amount: "0", currency: "USD" },
        single_min: undefined,
        single_max: undefined,
        daily_limit: undefined,
        enabled: true,
      },
    ],
  });
});

it("refuses a merchant the contract or the plugin would refuse", () => {
  const edit = (change: Record<string, string>) =>
    readMerchant({ ...merchantValues, ...change }, undefined, merchantWords);
  expect(edit({ name: " " })).toEqual({ problem: "name" });
  expect(edit({ ttl_seconds: "30" })).toEqual({ problem: "number" });
  expect(edit({ settings: "[]" })).toEqual({ problem: "settings" });
  expect(edit({ "method-0-method": "Alipay" })).toEqual({ problem: "methods" });
  expect(edit({ "method-0-percent": "50.0001" })).toEqual({ problem: "fee" });
  expect(edit({ "method-0-fixed": "" })).toEqual({ problem: "amount" });
  expect(edit({ "method-0-min": "2", "method-0-max": "1" })).toEqual({ problem: "range" });
  expect(edit({ key: "" })).toEqual({ problem: "secret" });
  expect(edit({ "method-0-method": "" })).toEqual({ problem: "methods" });
});

it("keeps the sealed key when an edit leaves both secret fields empty", () => {
  const read = readMerchant({ ...merchantValues, key: "", rev: "4" }, { rev: 4, kind: "easypay" }, merchantWords);
  if (isProblem(read)) {
    throw new Error(read.problem);
  }
  expect(read.rev).toBe(4);
  expect(read.secrets).toBeUndefined();
});
