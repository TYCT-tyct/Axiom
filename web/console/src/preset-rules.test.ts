import type { Schemas } from "@axiom/api";
import { expect, it } from "vitest";
import { draftOf, newOverride, rulesOf, summaryOf } from "./preset-rules";

const rules = (value: object) => value as Schemas["RoutingRules"];

// What the editor reads it writes back unchanged, members it does not edit included, so saving without a change keeps
// the version and a newer node's member survives an older page.
it.each([
  { case: "an empty default", stored: { default: {} } },
  {
    case: "every member the editor writes",
    stored: {
      default: {
        sort: "price",
        classes: ["official", "cloud"],
        policies: ["no_retention"],
        verified_only: true,
        min_score: 90,
        min_score_confidence: 0.8,
        max_price: { prompt: "3", completion: "15", request: "0.01" },
      },
      rules: [
        { author: "anthropic", sort: "quality", classes: ["official"] },
        { model: "anthropic/claude-opus-4.1", max_price: { prompt: "2" } },
      ],
    },
  },
  { case: "members it does not know", stored: { default: { sort: "price", future: [1] }, later: true } },
])("$case comes back as it was", ({ stored }) => {
  expect(rulesOf(draftOf(rules(stored)))).toEqual({ rules: stored });
});

it("leaves out what writes nothing and what only the platform may hold", () => {
  const draft = draftOf(
    rules({
      default: { classes: ["*"], verified_only: false, max_price: {} },
      advanced: { order: ["g1"] },
    }),
  );
  expect(rulesOf(draft)).toEqual({ rules: { default: {} } });
});

it.each([
  { case: "a score above 100", edit: { minScore: "101" }, field: "minScore" },
  { case: "a confidence above 1", edit: { minConfidence: "1.5" }, field: "minConfidence" },
  { case: "a negative score", edit: { minScore: "-1" }, field: "minScore" },
  { case: "a price in words", edit: { maxPrice: { prompt: "three", completion: "", request: "" } }, field: "prompt" },
  {
    case: "a price with a comma",
    edit: { maxPrice: { prompt: "", completion: "1,5", request: "" } },
    field: "completion",
  },
])("refuses $case in the default rule", ({ edit, field }) => {
  const draft = draftOf(rules({ default: {} }));
  expect(rulesOf({ ...draft, base: { ...draft.base, ...edit } })).toEqual({ invalid: { rule: "default", field } });
});

it("refuses an override that names nothing, by its place", () => {
  const draft = draftOf(rules({ default: {}, rules: [{ author: "openai" }, { model: " " }] }));
  expect(rulesOf(draft)).toEqual({ invalid: { rule: 1, field: "name" } });
});

it("writes an override's target and trims its name", () => {
  const draft = draftOf(rules({ default: {} }));
  draft.overrides.push({ ...newOverride(), target: "model", name: " openai/gpt-5 ", verifiedOnly: true });
  expect(rulesOf(draft)).toEqual({ rules: { default: {}, rules: [{ model: "openai/gpt-5", verified_only: true }] } });
});

it("sums a preset up", () => {
  expect(summaryOf(rules({ default: {}, rules: [{ author: "openai", max_price: { prompt: "1" } }] }))).toEqual({
    sort: "balanced",
    classes: null,
    priced: true,
    verifiedOnly: false,
    overrides: 1,
  });
});
