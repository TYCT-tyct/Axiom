import type { AdminSchemas } from "@axiom/api";
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
  if (min.value && max.value && BigInt(min.value.amount) > BigInt(max.value.amount)) {
    return { problem: words.range };
  }
  let buffer: number | undefined;
  if (bufferOffered) {
    const typed = (values.top_up_buffer_ppm ?? "").trim();
    if (typed !== "") {
      const ppm = parseBufferPercent(typed);
      if (ppm === null) {
        return { problem: words.buffer };
      }
      buffer = ppm;
    }
  }
  return {
    open: values.open === "true",
    credits_min: min.value,
    credits_max: max.value,
    ...(buffer === undefined ? {} : { top_up_buffer_ppm: buffer }),
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

function readMethod(
  values: Record<string, string>,
  index: number,
  currency: string,
  words: MerchantWords,
): AdminSchemas["MerchantMethodInput"] | Problem {
  const method = (values[`method-${index}-method`] ?? "").trim();
  const upstream = (values[`method-${index}-upstream`] ?? "").trim();
  if (!/^[a-z][a-z0-9_.]{0,31}$/.test(method) || !upstream || [...upstream].length > 64) {
    return { problem: words.methods };
  }
  const ppm = parsePercent(values[`method-${index}-percent`] ?? "", 500_000);
  if (ppm === null) {
    return { problem: words.fee };
  }
  const fixed = readMoney(values[`method-${index}-fixed`] ?? "", currency, true);
  const min = readMoney(values[`method-${index}-min`] ?? "", currency, false);
  const max = readMoney(values[`method-${index}-max`] ?? "", currency, false);
  const daily = readMoney(values[`method-${index}-daily`] ?? "", currency, false);
  if (!fixed.ok || !min.ok || !max.ok || !daily.ok) {
    return { problem: words.amount };
  }
  if (min.value && max.value && BigInt(min.value.amount) > BigInt(max.value.amount)) {
    return { problem: words.range };
  }
  return {
    method,
    upstream,
    fee_ppm: ppm,
    fee_fixed: fixed.value as AdminSchemas["Money"],
    single_min: min.value,
    single_max: max.value,
    daily_limit: daily.value,
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

function readSecrets(values: Record<string, string>, creating: boolean): { [key: string]: string } | null | undefined {
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
export function readMerchant(
  values: Record<string, string>,
  merchant: AdminSchemas["Merchant"] | undefined,
  words: MerchantWords,
): AdminSchemas["MerchantInput"] | Problem {
  const name = (values.name ?? "").trim();
  if (!name || [...name].length > 100) {
    return { problem: words.name };
  }
  const ttl = readWhole(values.ttl_seconds ?? "", 60, 86_400);
  if (ttl === null) {
    return { problem: words.number };
  }
  const sort = readSort(values.sort ?? "");
  if (sort === null) {
    return { problem: words.number };
  }
  const settings = readChannelSettings(values.settings ?? "");
  if (settings === null) {
    return { problem: words.settings };
  }
  const methods = readMethods(values, values.currency ?? "", words);
  if ("problem" in methods) {
    return methods;
  }
  const secrets = readSecrets(values, merchant === undefined);
  if (secrets === null) {
    return { problem: words.secret };
  }
  return withChangeFields(
    {
      kind: values.kind ?? merchant?.kind ?? "easypay",
      name,
      enabled: values.enabled === "true",
      currency: values.currency ?? "",
      ttl_seconds: ttl,
      refund_enabled: values.refund_enabled === "true",
      user_refund: values.user_refund === "true",
      sort,
      settings,
      methods,
    },
    merchant?.rev,
    secrets,
  );
}
