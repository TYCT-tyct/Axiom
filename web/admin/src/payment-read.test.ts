import { ApiError } from "@axiom/api";
import { expect, it } from "vitest";
import { isProblem } from "./change";
import { mailSavedRestart } from "./mail-settings";
import {
  bufferRefused,
  formatPercent,
  parseBufferPercent,
  parsePercent,
  readMerchant,
  readPayments,
  withoutBuffer,
} from "./payment-read";
import { nameEditable, siteSettingsInput } from "./site-info";

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

const alipayValues = {
  ...merchantValues,
  kind: "alipay",
  settings: '{"app_id":"2021000000000001","seller_id":"2088000000000001"}',
  key: "",
  private_key: "app-private-key",
  public_key: "alipay-public-key",
  old_public_key: "",
};

it("reads a new Alipay merchant with its key pair", () => {
  const read = readMerchant(alipayValues, undefined, merchantWords);
  if (isProblem(read)) {
    throw new Error(read.problem);
  }
  expect(read.kind).toBe("alipay");
  expect(read.settings).toEqual({ app_id: "2021000000000001", seller_id: "2088000000000001" });
  expect(read.secrets).toEqual({ private_key: "app-private-key", public_key: "alipay-public-key" });
});

it("requires both Alipay keys when any secret is supplied", () => {
  expect(readMerchant({ ...alipayValues, private_key: "" }, undefined, merchantWords)).toEqual({ problem: "secret" });
  expect(readMerchant({ ...alipayValues, public_key: "" }, undefined, merchantWords)).toEqual({ problem: "secret" });
  expect(
    readMerchant({ ...alipayValues, private_key: "", public_key: "", old_public_key: "" }, undefined, merchantWords),
  ).toEqual({ problem: "secret" });
});

it("keeps an Alipay merchant's channel and sealed secrets on edit", () => {
  const { kind: _kind, ...withoutKind } = alipayValues;
  const read = readMerchant(
    { ...withoutKind, private_key: "", public_key: "", old_public_key: "" },
    { rev: 4, kind: "alipay" },
    merchantWords,
  );
  if (isProblem(read)) {
    throw new Error(read.problem);
  }
  expect(read.kind).toBe("alipay");
  expect(read.rev).toBe(4);
  expect(read.secrets).toBeUndefined();
});

it("reads an Alipay rotation with the old public key", () => {
  const read = readMerchant({ ...alipayValues, old_public_key: "old-public-key" }, undefined, merchantWords);
  if (isProblem(read)) {
    throw new Error(read.problem);
  }
  expect(read.secrets).toEqual({
    private_key: "app-private-key",
    public_key: "alipay-public-key",
    old_public_key: "old-public-key",
  });
});

const stripeValues = {
  ...merchantValues,
  kind: "stripe",
  settings: "{}",
  key: "",
  secret_key: "sk_test_key",
  webhook_secret: "whsec_webhook",
  old_webhook_secret: "",
  "method-0-method": "card",
  "method-0-upstream": "card",
};

it("reads a new Stripe merchant with both required secrets", () => {
  const read = readMerchant(stripeValues, undefined, merchantWords);
  if (isProblem(read)) {
    throw new Error(read.problem);
  }
  expect(read.kind).toBe("stripe");
  expect(read.settings).toEqual({});
  expect(read.secrets).toEqual({ secret_key: "sk_test_key", webhook_secret: "whsec_webhook" });
});

it("requires both Stripe secrets when any is supplied", () => {
  expect(readMerchant({ ...stripeValues, secret_key: "" }, undefined, merchantWords)).toEqual({ problem: "secret" });
  expect(readMerchant({ ...stripeValues, webhook_secret: "" }, undefined, merchantWords)).toEqual({
    problem: "secret",
  });
  expect(
    readMerchant(
      { ...stripeValues, secret_key: "", webhook_secret: "", old_webhook_secret: "" },
      undefined,
      merchantWords,
    ),
  ).toEqual({ problem: "secret" });
});

it("keeps a Stripe merchant's sealed secrets on edit and reads a webhook rotation", () => {
  const { kind: _kind, ...withoutKind } = stripeValues;
  const kept = readMerchant(
    { ...withoutKind, secret_key: "", webhook_secret: "", old_webhook_secret: "" },
    { rev: 4, kind: "stripe" },
    merchantWords,
  );
  if (isProblem(kept)) {
    throw new Error(kept.problem);
  }
  expect(kept.kind).toBe("stripe");
  expect(kept.rev).toBe(4);
  expect(kept.secrets).toBeUndefined();
  const rotated = readMerchant({ ...stripeValues, old_webhook_secret: "whsec_old" }, undefined, merchantWords);
  if (isProblem(rotated)) {
    throw new Error(rotated.problem);
  }
  expect(rotated.secrets).toEqual({
    secret_key: "sk_test_key",
    webhook_secret: "whsec_webhook",
    old_webhook_secret: "whsec_old",
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

it("drops only the buffer for a node whose contract refuses it", () => {
  const settings = { open: true, credits_min: { amount: "1", currency: "USD" }, top_up_buffer_ppm: 20_000 };
  expect(withoutBuffer(settings)).toEqual({ open: true, credits_min: { amount: "1", currency: "USD" } });
  const refusal = new ApiError(400, null, {
    body: {
      error: {
        code: "invalid_request",
        message: "the settings are invalid",
        fields: [{ pointer: "/settings/top_up_buffer_ppm", message: "unknown field" }],
      },
    },
  });
  expect(bufferRefused(refusal)).toBe(true);
  expect(bufferRefused(new ApiError(422, null, { body: { error: { code: "unprocessable", message: "no" } } }))).toBe(
    false,
  );
});

it("sends only the name so a registration change made meanwhile survives", () => {
  expect(siteSettingsInput("Main")).toEqual({ name: "Main" });
});

it("leaves the name read-only on a node that does not report the name-only save", () => {
  expect(nameEditable({ name: "Main", registration: "invite" })).toBe(false);
  expect(nameEditable({ name: "Main", registration: "invite", name_editable: true })).toBe(true);
});

it("shows the restart a database mail save needs", () => {
  expect(mailSavedRestart({ restart_required: true })).toBe(true);
  expect(mailSavedRestart({})).toBe(false);
});
