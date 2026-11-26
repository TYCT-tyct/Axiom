import type { AdminSchemas } from "@axiom/api";
import type { TFunction } from "i18next";
import { isProblem, type Problem } from "../change";
import { readDeployments, readKeys, readRegions, validRegion } from "./key-fields";

// The channel types as listSupplierTypes describes them (docs/domains/suppliers-direct.md「渠道类型」). The registry is
// the only list of types: nothing here names one, and every type is drawn from its descriptor.

export type SupplierType = AdminSchemas["SupplierType"];
type TypeField = AdminSchemas["SupplierTypeField"];
type T = TFunction<"admin">;

const typeGroups = [
  "official",
  "domestic",
  "cloud",
  "relay",
] as const satisfies readonly SupplierType["group"][];

// The Gemini format is not sent yet (suppliers-direct.md「决策与理由」), so a type whose family a person chooses offers
// OpenAI's or Anthropic's.
export const choosableFamilies = (type: SupplierType) =>
  type.families.filter((family): family is "openai" | "anthropic" => family !== "gemini");

// A type has nothing to sell yet when every one of its price entries counts no model; a type that sells any maker's
// models gives no count and is never greyed.
export const unsellable = (type: SupplierType) =>
  type.entries.length > 0 && type.entries.every((entry) => entry.models === 0);

// The most models one of the type's entries sells, or none when it gives no count.
export function modelCount(type: SupplierType) {
  const counts = type.entries.flatMap((entry) => (entry.models === undefined ? [] : [entry.models]));
  return counts.length > 0 ? Math.max(...counts) : undefined;
}

// The types a query finds, by name, vendor or kind, under their groups in the groups' order; a group nothing is found
// in is left out. The list keeps the server's order (group, then name) within a group.
export function findTypes(types: readonly SupplierType[], query: string) {
  const q = query.trim().toLowerCase();
  const found = types.filter((type) =>
    [type.name, type.vendor ?? "", type.kind].some((word) => word.toLowerCase().includes(q)),
  );
  return typeGroups
    .map((group) => ({ group, types: found.filter((type) => type.group === group) }))
    .filter((section) => section.types.length > 0);
}

// The line under a type's name, made from its descriptor, never from words per type: the families it speaks, the
// currency of its default entry (or that a person gives the address and currency), and whether it reads balances.
export function typeLine(type: SupplierType, t: T) {
  if (type.mode === "relay") return t("upstream.types.relayLine");
  const families = type.families.map((family) => t(`upstream.addDialog.formats.${family}`)).join(" / ");
  const first = type.entries[0];
  return [
    families,
    first ? t("upstream.types.settles", { currency: first.currency }) : t("upstream.types.ownOrigin"),
    type.reads_balance ? t("upstream.types.balance") : null,
  ]
    .filter(Boolean)
    .join(" · ");
}

// A field's words by its name, from the page's vocabulary; a name the page does not know yet shows as itself.
export function fieldWords(t: T, name: string) {
  const at = `upstream.fields.${name}` as "upstream.fields.keys";
  return { label: t(`${at}.label`, { defaultValue: name }), hint: t(`${at}.hint`, { defaultValue: "" }) || undefined };
}

export const formLabel = (t: T, name: string) =>
  t(`upstream.types.forms.${name}` as "upstream.types.forms.api_key", { defaultValue: name });

// The form named, or the type's first.
export const formOf = (type: SupplierType, name?: string) =>
  type.forms.find((form) => form.name === name) ?? type.forms[0];

// What the fields came to: the request's fields map (absent when nothing in it was filled in), and its own members.
type FieldsRead = {
  fields?: Record<string, string>;
  keys?: string[];
  regions?: string[];
  deployments?: NonNullable<ReturnType<typeof readDeployments>>;
};

// A pattern is the server's (Go's regular expressions); one the browser cannot compile is left to the server to check.
function matches(pattern: string, value: string) {
  try {
    return new RegExp(pattern).test(value);
  } catch {
    return true;
  }
}

function isJson(text: string) {
  try {
    JSON.parse(text);
    return true;
  } catch {
    return false;
  }
}

type Read = Omit<FieldsRead, "fields"> & { value?: string };
type Say = (problem: "fieldFormat" | "fieldJson") => Problem;

// How each kind of field is read from what was typed (its trimmed value, or the whole form for the kinds that read
// several lines): into one of the request's own members, or as the value sent under the field's name.
const readers: Record<
  TypeField["kind"],
  (typed: string, field: TypeField, values: Record<string, string>, t: T, say: Say) => Read | Problem
> = {
  keys: (_, __, values, t) => {
    const keys = readKeys(values, t);
    return isProblem(keys) ? keys : { keys };
  },
  regions: (_, __, values, t) => {
    const regions = readRegions(values);
    return regions.length <= 20 && regions.every(validRegion)
      ? { regions }
      : { problem: t("upstream.addDialog.problems.regions") };
  },
  deployments: (_, __, values, t) => {
    const deployments = readDeployments(values);
    return deployments ? { deployments } : { problem: t("upstream.addDialog.problems.deployments") };
  },
  json: (typed, _, __, ___, say) => (isJson(typed) ? { value: typed } : say("fieldJson")),
  secret: (typed, field, _, __, say) => patterned(typed, field, say),
  text: (typed, field, _, __, say) => patterned(typed, field, say),
};

const patterned = (typed: string, field: TypeField, say: Say) =>
  field.pattern && !matches(field.pattern, typed) ? say("fieldFormat") : { value: typed };

// What was typed into the fields given, by each one's kind: the keys, regions and deployments the request carries as
// its own members, and the rest by name. An optional field left empty is not sent; the keys box says itself what is
// wrong with it when empty.
export function readFields(list: readonly TypeField[], values: Record<string, string>, t: T): FieldsRead | Problem {
  let out: FieldsRead = {};
  const fields: Record<string, string> = {};
  for (const field of list) {
    const say = (key: "fieldMissing" | "fieldFormat" | "fieldJson") => ({
      problem: t(`upstream.addDialog.problems.${key}`, { label: fieldWords(t, field.name).label }),
    });
    const typed = (values[field.name] ?? "").trim();
    if (typed === "" && field.kind !== "keys") {
      if (field.required) return say("fieldMissing");
      continue;
    }
    const read = readers[field.kind](typed, field, values, t, say);
    if (isProblem(read)) return read;
    const { value, ...members } = read;
    if (value !== undefined) fields[field.name] = value;
    out = { ...out, ...members };
  }
  return { ...out, fields: Object.keys(fields).length > 0 ? fields : undefined };
}
