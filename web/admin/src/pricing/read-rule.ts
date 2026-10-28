import { isProblem, type Problem } from "../change";
import { type Invalid, readSchedule } from "../models/price-fields";
import type { Rule } from "./rule-fields";

type Words = Record<"target" | "audience" | "ratio" | "multiplier", string> & Invalid;

const decimal = /^(0|[1-9]\d{0,17})(\.\d{1,15})?$/;

// Which field holds the ID of the thing a rule prices depends on the kind of target.
function targetId(values: Record<string, string>, kind: Rule["target"]["kind"]) {
  if (kind === "model") {
    return values.model_id;
  }
  return kind === "author" ? values.target_author : values.target_id;
}

function readTarget(values: Record<string, string>, words: Words): Rule["target"] | Problem {
  const kind = values.target_kind as Rule["target"]["kind"];
  if (kind === "all") {
    return { kind };
  }
  const id = targetId(values, kind)?.trim();
  return id ? { kind, id } : { problem: words.target };
}

function readAudience(values: Record<string, string>, words: Words): Rule["audience"] | Problem {
  const kind = values.audience_kind as Rule["audience"]["kind"];
  if (kind === "all") {
    return { kind };
  }
  const id = (kind === "tier" ? values.audience_tier : values.audience_account)?.trim();
  return id ? { kind, id } : { problem: words.audience };
}

function readMethod(
  values: Record<string, string>,
  words: Words,
  existing: Rule["method"] | undefined,
): Rule["method"] | Problem {
  const kind = values.method_kind as Rule["method"]["kind"];
  if (kind === "cost_plus") {
    const ratio = (values.ratio ?? "").trim();
    const key = values.basis === "margin" ? "margin_ratio" : "markup_ratio";
    return decimal.test(ratio) ? { kind, [key]: ratio } : { problem: words.ratio };
  }
  if (kind === "reference_multiplier") {
    const multiplier = (values.multiplier ?? "").trim();
    return decimal.test(multiplier)
      ? { kind, multiplier, currency_conversion: values.currency_conversion === "exchange" ? "exchange" : "face" }
      : { problem: words.multiplier };
  }
  const schedule = readSchedule(values, existing?.schedule, words);
  return isProblem(schedule) ? schedule : { kind: "fixed", schedule };
}

// readRule makes the rule from the typed fields, or says what is wrong with them. Every ratio is decimal text. It never
// carries constraints: this release does not apply them (#234), so the server refuses any, and saving clears a stored one.
export function readRule(values: Record<string, string>, words: Words, existing?: Rule): Rule | Problem {
  const target = readTarget(values, words);
  const audience = readAudience(values, words);
  const method = readMethod(values, words, existing?.method);
  for (const part of [target, audience, method]) {
    if (isProblem(part)) {
      return part;
    }
  }
  return {
    enabled: values.enabled === "on",
    target: target as Rule["target"],
    audience: audience as Rule["audience"],
    method: method as Rule["method"],
    constraints: {},
  };
}
