import type { TFunction } from "i18next";
import { describe, expect, it } from "vitest";
import { findTypes, modelCount, readFields, type SupplierType, typeLine, unsellable } from "./supplier-types";

const type = (kind: string, over: Partial<SupplierType> = {}): SupplierType => ({
  kind,
  mode: "direct",
  name: kind,
  group: "official",
  entries: [],
  families: ["openai"],
  forms: [],
  fields: [],
  lists_models: true,
  reads_balance: false,
  ...over,
});
const entry = (id: string, currency: string, models?: number) => ({
  id,
  origin: `https://${id}.test`,
  currency,
  models,
});

// The words are the keys with their values, so the line's makeup shows without the locales.
const t = ((key: string, values?: Record<string, unknown>) =>
  values && "currency" in values ? `${key}(${values.currency})` : key) as unknown as TFunction<"admin">;

const types = [
  type("openai", { name: "OpenAI", vendor: "openai" }),
  type("relaybox", { mode: "relay", name: "Relay Box", group: "relay", families: [] }),
  type("deepseek", { name: "DeepSeek", vendor: "deepseek", group: "domestic" }),
  type("vertex", { mode: "cloud", name: "Vertex AI", vendor: "google-cloud", group: "cloud" }),
];

describe("finding a type", () => {
  it.each([
    [
      "",
      [
        ["official", ["openai"]],
        ["domestic", ["deepseek"]],
        ["cloud", ["vertex"]],
        ["relay", ["relaybox"]],
      ],
    ],
    ["  DEEP ", [["domestic", ["deepseek"]]]],
    ["google", [["cloud", ["vertex"]]]],
    ["relaybox", [["relay", ["relaybox"]]]],
    ["nothing", []],
  ])("%j finds the types by name, vendor or kind under their groups in order", (query, want) => {
    expect(findTypes(types, query).map((s) => [s.group, s.types.map((x) => x.kind)])).toEqual(want);
  });
});

describe("a type's models", () => {
  it.each([
    ["no entries", [], false, undefined],
    ["entries without counts", [entry("a", "USD")], false, undefined],
    ["every entry at 0", [entry("a", "CNY", 0), entry("b", "USD", 0)], true, 0],
    ["one entry with models", [entry("a", "CNY", 0), entry("b", "USD", 12)], false, 12],
  ])("%s", (_, entries, greyed, count) => {
    const of = type("x", { entries });
    expect([unsellable(of), modelCount(of)]).toEqual([greyed, count]);
  });
});

describe("a type's line", () => {
  it.each([
    [type("x", { mode: "relay", families: [] }), "upstream.types.relayLine"],
    [
      type("x", {
        families: ["openai", "anthropic"],
        entries: [entry("cn", "CNY"), entry("g", "USD")],
        reads_balance: true,
      }),
      "upstream.addDialog.formats.openai / upstream.addDialog.formats.anthropic · upstream.types.settles(CNY) · upstream.types.balance",
    ],
    [type("x"), "upstream.addDialog.formats.openai · upstream.types.ownOrigin"],
  ])("is made of its descriptor's facts", (of, line) => {
    expect(typeLine(of, t)).toBe(line);
  });
});

describe("reading a type's fields", () => {
  const fields: SupplierType["fields"] = [
    { name: "keys", kind: "keys", required: true },
    { name: "organization", kind: "text", required: false, pattern: "^org-[A-Za-z0-9]{1,64}$" },
    { name: "service_account_json", kind: "json", required: false },
    { name: "regions", kind: "regions", required: false },
  ];
  it.each([
    [{ keys: "sk-aaaaaaaa\nsk-bbbbbbbb" }, { keys: ["sk-aaaaaaaa", "sk-bbbbbbbb"], fields: undefined }],
    [
      { keys: "sk-aaaaaaaa", organization: " org-abc ", regions: "us-east-1, eu-west-1" },
      { keys: ["sk-aaaaaaaa"], regions: ["us-east-1", "eu-west-1"], fields: { organization: "org-abc" } },
    ],
    [{ keys: "sk-aaaaaaaa", organization: "acme" }, { problem: "upstream.addDialog.problems.fieldFormat" }],
    [{ keys: "sk-aaaaaaaa", service_account_json: "{" }, { problem: "upstream.addDialog.problems.fieldJson" }],
    [{ keys: "sk-aaaaaaaa", regions: "US_EAST" }, { problem: "upstream.addDialog.problems.regions" }],
    [{}, { problem: "upstream.addDialog.problems.keys" }],
  ])("%j", (values, want) => {
    expect(readFields(fields, values, t)).toEqual(want);
  });
});
