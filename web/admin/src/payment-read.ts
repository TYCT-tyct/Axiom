import { type AdminSchemas, ApiError } from "@axiom/api";
import { parseMoney } from "@axiom/ui";
import type { Problem } from "./change";

// A percentage as the contract stores it: millionths, so 3% is 30000. At most four decimals of a percent (one ppm);
// integer math keeps 0.1 + 0.2 out of a value the server would refuse.
export function parsePercent(text: string, max: number): number | null {
  const typed = text.trim();
  const match = /^(?:0|[1-9]\d*)(?:\.(\d{1,4}))?$/.exec(typed);
  if (!match) {
    return null;
  }
  const [whole, fraction = ""] = typed.split(".");
  const ppm = Number(whole) * 10_000 + Number(fraction.padEnd(4, "0"));
  return Number.isSafeInteger(ppm) && ppm <= max ? ppm : null;
}

export function parseBufferPercent(text: string): number | null {
  return parsePercent(text, 100_000);
}

export function methodFallback(kind: string): { method: string; upstream: string } {
  return kind === "wechatpay" ? { method: "wxpay", upstream: "native" } : { method: "alipay", upstream: "alipay" };
}

// defaultMethodRows follows a kind change only for rows still holding the old kind's untouched defaults. A row the
// operator has edited stays as it is, even when it happens to be editable for the new kind too.
export function defaultMethodRows<T extends { method: string; upstream: string }>(
  rows: T[],
  from: string,
  to: string,
): T[] {
  const old = methodFallback(from);
  const next = methodFallback(to);
  return rows.map((row) => (row.method === old.method && row.upstream === old.upstream ? { ...row, ...next } : row));
}

export function formatPercent(ppm: number): string {
  const whole = Math.floor(ppm / 10_000);
  const fraction = ppm % 10_000;
  if (fraction === 0) {
    return String(whole);
  }
  return `${whole}.${String(fraction).padStart(4, "0").replace(/0+$/, "")}`;
}

// The money fields are exact decimal text on the wire and billionths in the form; empty means the limit is not set.
function readMoney(text: string, currency: string, required: boolean): { value?: AdminSchemas["Money"]; ok: boolean } {
  const typed = text.trim();
  if (!typed) {
    return required ? { ok: false } : { ok: true };
  }
  const amount = parseMoney(typed);
  if (amount === null || BigInt(amount) < 0n) {
    return { ok: false };
  }
  return { value: { amount, currency }, ok: true };
}

function readWhole(text: string, min: number, max: number): number | null {
  const typed = text.trim();
  return /^\d+$/.test(typed) && Number(typed) >= min && Number(typed) <= max ? Number(typed) : null;
}

function readSort(text: string): number | null {
  const typed = text.trim();
  return typed === "" ? 0 : readWhole(typed, 0, 2_147_483_647);
}

export type PaymentWords = { amount: string; range: string; buffer: string };

// readPayments makes the payment settings from the dialog's fields. A buffer is only offered when the node answered
// with one; an older node has no field for it, and leaving it out keeps whatever it has.
function rangeReversed(min: AdminSchemas["Money"] | undefined, max: AdminSchemas["Money"] | undefined): boolean {
  return min !== undefined && max !== undefined && BigInt(min.amount) > BigInt(max.amount);
}

function readBuffer(
  values: Record<string, string>,
  bufferOffered: boolean,
  words: PaymentWords,
): { ppm?: number; problem?: string } {
  if (!bufferOffered || (values.top_up_buffer_ppm ?? "").trim() === "") {
    return {};
  }
  const ppm = parseBufferPercent((values.top_up_buffer_ppm ?? "").trim());
  return ppm === null ? { problem: words.buffer } : { ppm };
}

export function readPayments(
  values: Record<string, string>,
  currency: string,
  bufferOffered: boolean,
  words: PaymentWords,
): AdminSchemas["PaymentSettingsUpdate"]["settings"] | Problem {
  const min = readMoney(values.credits_min ?? "", currency, false);
  const max = readMoney(values.credits_max ?? "", currency, false);
  if (!min.ok || !max.ok) {
    return { problem: words.amount };
  }
  if (rangeReversed(min.value, max.value)) {
    return { problem: words.range };
  }
  const buffer = readBuffer(values, bufferOffered, words);
  if (buffer.problem !== undefined) {
    return { problem: buffer.problem };
  }
  return {
    open: values.open === "true",
    credits_min: min.value,
    credits_max: max.value,
    ...(buffer.ppm === undefined ? {} : { top_up_buffer_ppm: buffer.ppm }),
  };
}

export type MerchantWords = {
  name: string;
  number: string;
  settings: string;
  secret: string;
  amount: string;
  fee: string;
  range: string;
  methods: string;
};

function readChannelSettings(text: string): { [key: string]: unknown } | null {
  try {
    const parsed: unknown = JSON.parse(text);
    return typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)
      ? (parsed as { [key: string]: unknown })
      : null;
  } catch {
    return null;
  }
}

function readMethodCodes(
  values: Record<string, string>,
  index: number,
  words: MerchantWords,
): { method: string; upstream: string } | Problem {
  const method = (values[`method-${index}-method`] ?? "").trim();
  const upstream = (values[`method-${index}-upstream`] ?? "").trim();
  if (!/^[a-z][a-z0-9_.]{0,31}$/.test(method) || !upstream || [...upstream].length > 64) {
    return { problem: words.methods };
  }
  return { method, upstream };
}

function readMethodAmounts(
  values: Record<string, string>,
  index: number,
  currency: string,
  words: MerchantWords,
): Pick<AdminSchemas["MerchantMethodInput"], "fee_fixed" | "single_min" | "single_max" | "daily_limit"> | Problem {
  const fixed = readMoney(values[`method-${index}-fixed`] ?? "", currency, true);
  const min = readMoney(values[`method-${index}-min`] ?? "", currency, false);
  const max = readMoney(values[`method-${index}-max`] ?? "", currency, false);
  const daily = readMoney(values[`method-${index}-daily`] ?? "", currency, false);
  if (!fixed.ok || !min.ok || !max.ok || !daily.ok) {
    return { problem: words.amount };
  }
  if (rangeReversed(min.value, max.value)) {
    return { problem: words.range };
  }
  return {
    fee_fixed: fixed.value as AdminSchemas["Money"],
    single_min: min.value,
    single_max: max.value,
    daily_limit: daily.value,
  };
}

function readMethod(
  values: Record<string, string>,
  index: number,
  currency: string,
  words: MerchantWords,
): AdminSchemas["MerchantMethodInput"] | Problem {
  const codes = readMethodCodes(values, index, words);
  if ("problem" in codes) {
    return codes;
  }
  const ppm = parsePercent(values[`method-${index}-percent`] ?? "", 500_000);
  if (ppm === null) {
    return { problem: words.fee };
  }
  const amounts = readMethodAmounts(values, index, currency, words);
  if ("problem" in amounts) {
    return amounts;
  }
  return {
    ...codes,
    ...amounts,
    fee_ppm: ppm,
    enabled: values[`method-${index}-enabled`] === "true",
  };
}

function readMethods(
  values: Record<string, string>,
  currency: string,
  words: MerchantWords,
): AdminSchemas["MerchantMethodInput"][] | Problem {
  const indices = [
    ...new Set(
      Object.keys(values)
        .map((key) => /^method-(\d+)-/.exec(key)?.[1])
        .filter((index): index is string => index !== undefined)
        .map(Number),
    ),
  ].sort((a, b) => a - b);
  if (indices.length === 0) {
    return { problem: words.methods };
  }
  const methods: AdminSchemas["MerchantMethodInput"][] = [];
  for (const index of indices) {
    const method = readMethod(values, index, currency, words);
    if ("problem" in method) {
      return method;
    }
    methods.push(method);
  }
  return methods;
}

function readSecrets(
  values: Record<string, string>,
  creating: boolean,
  kind: string,
): { [key: string]: string } | null | undefined {
  switch (kind) {
    case "stripe":
      return readStripeSecrets(values, creating);
    case "alipay":
      return readAlipaySecrets(values, creating);
    case "wechatpay":
      return readWechatSecrets(values, creating);
    default:
      return readEasyPaySecrets(values, creating);
  }
}

function readStripeSecrets(
  values: Record<string, string>,
  creating: boolean,
): { [key: string]: string } | null | undefined {
  const secretKey = (values.secret_key ?? "").trim();
  const webhook = (values.webhook_secret ?? "").trim();
  const oldWebhook = (values.old_webhook_secret ?? "").trim();
  if (!secretKey && !webhook && !oldWebhook) {
    return creating ? null : undefined;
  }
  // The server replaces the sealed map rather than merging into it, so a partial rotation would drop the other secret.
  if (!secretKey || !webhook) {
    return null;
  }
  // Both required secrets are non-empty past the guard, so only the rotating one needs a condition.
  return {
    secret_key: secretKey,
    webhook_secret: webhook,
    ...(oldWebhook ? { old_webhook_secret: oldWebhook } : {}),
  };
}

function readAlipaySecrets(
  values: Record<string, string>,
  creating: boolean,
): { [key: string]: string } | null | undefined {
  const privateKey = (values.private_key ?? "").trim();
  const publicKey = (values.public_key ?? "").trim();
  const oldPublicKey = (values.old_public_key ?? "").trim();
  if (!privateKey && !publicKey && !oldPublicKey) {
    return creating ? null : undefined;
  }
  if (!privateKey || !publicKey) {
    return null;
  }
  return {
    private_key: privateKey,
    public_key: publicKey,
    ...(oldPublicKey ? { old_public_key: oldPublicKey } : {}),
  };
}

function readWechatSecrets(
  values: Record<string, string>,
  creating: boolean,
): { [key: string]: string } | null | undefined {
  const names = [
    "private_key",
    "api_v3_key",
    "platform_certificate",
    "platform_public_key",
    "old_platform_certificate",
    "old_platform_public_key",
  ];
  const secrets: { [key: string]: string } = {};
  for (const name of names) {
    const value = (values[name] ?? "").trim();
    if (value) {
      secrets[name] = value;
    }
  }
  if (Object.keys(secrets).length === 0) {
    return creating ? null : undefined;
  }
  if (!secrets.private_key || !secrets.api_v3_key || (!secrets.platform_certificate && !secrets.platform_public_key)) {
    return null;
  }
  return secrets;
}

function readEasyPaySecrets(
  values: Record<string, string>,
  creating: boolean,
): { [key: string]: string } | null | undefined {
  const key = (values.key ?? "").trim();
  const oldKey = (values.old_key ?? "").trim();
  if ((creating || oldKey) && !key) {
    return null;
  }
  if (!key && !oldKey) {
    return undefined;
  }
  return { ...(key ? { key } : {}), ...(oldKey ? { old_key: oldKey } : {}) };
}

function withChangeFields(
  input: AdminSchemas["MerchantInput"],
  rev: number | undefined,
  secrets: { [key: string]: string } | undefined,
): AdminSchemas["MerchantInput"] {
  return {
    ...input,
    ...(rev === undefined ? {} : { rev }),
    ...(secrets === undefined ? {} : { secrets }),
  };
}

// readMerchant makes a create or update request from the dialog's fields, one method per method-<i>-* group the form
// holds. The server checks the plugin's own settings; this checks what the contract states before the request leaves.
function readMerchantBasics(
  values: Record<string, string>,
  merchant: AdminSchemas["Merchant"] | undefined,
  words: MerchantWords,
):
  | Pick<
      AdminSchemas["MerchantInput"],
      "kind" | "name" | "enabled" | "currency" | "ttl_seconds" | "refund_enabled" | "user_refund" | "sort"
    >
  | Problem {
  const name = (values.name ?? "").trim();
  if (!name || [...name].length > 100) {
    return { problem: words.name };
  }
  const ttl = readWhole(values.ttl_seconds ?? "", 60, 86_400);
  const sort = readSort(values.sort ?? "");
  if (ttl === null || sort === null) {
    return { problem: words.number };
  }
  return {
    kind: merchant?.kind ?? (values.kind || "easypay"),
    name,
    enabled: values.enabled === "true",
    currency: values.currency ?? "",
    ttl_seconds: ttl,
    refund_enabled: values.refund_enabled === "true",
    user_refund: values.user_refund === "true",
    sort,
  };
}

export function readMerchant(
  values: Record<string, string>,
  merchant: AdminSchemas["Merchant"] | undefined,
  words: MerchantWords,
): AdminSchemas["MerchantInput"] | Problem {
  const basics = readMerchantBasics(values, merchant, words);
  if ("problem" in basics) {
    return basics;
  }
  const settings = readChannelSettings(values.settings ?? "");
  if (settings === null) {
    return { problem: words.settings };
  }
  const methods = readMethods(values, values.currency ?? "", words);
  if ("problem" in methods) {
    return methods;
  }
  // Changing the plugin changes which secret names the server can bind; the old ones cannot carry over.
  const secrets = readSecrets(values, merchant === undefined || merchant.kind !== basics.kind, basics.kind);
  if (secrets === null) {
    return { problem: words.secret };
  }
  return withChangeFields({ ...basics, settings, methods }, merchant?.rev, secrets);
}

// withoutBuffer removes the field an old node's contract refuses; the rest of the settings are unchanged.
export function withoutBuffer(
  settings: AdminSchemas["PaymentSettingsUpdate"]["settings"],
): AdminSchemas["PaymentSettingsUpdate"]["settings"] {
  const { top_up_buffer_ppm: _, ...rest } = settings;
  return rest;
}

// bufferRefused is the old node's 400 for top_up_buffer_ppm under additionalProperties: false.
export function bufferRefused(error: unknown): boolean {
  return (
    error instanceof ApiError &&
    error.status === 400 &&
    error.code === "invalid_request" &&
    error.fields.some(
      ({ pointer, message }) => pointer === "/settings/top_up_buffer_ppm" || message.includes("top_up_buffer_ppm"),
    )
  );
}
