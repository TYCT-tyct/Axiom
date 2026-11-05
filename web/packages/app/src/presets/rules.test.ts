import type { Schemas } from "@axiom/api";
import { expect, it } from "vitest";
import { draftOf, newOverride, rulesOf, summaryOf } from "./rules";

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
        policies: ["no_retention", "may_train"],
        verified_only: true,
        max_price: { prompt: "3", completion: "15", request: "0.01" },
      },
      rules: [
        { author: "anthropic", sort: "quality", classes: ["official"] },
        { model: "anthropic/claude-opus-4.1", max_price: { prompt: "2" } },
      ],
    },
  },
  {
    case: "prices in every form the server reads",
    stored: { default: { max_price: { prompt: "1e3", completion: "0.1000000000000000", request: "2.5E-3" } } },
  },
  { case: "members it does not know", stored: { default: { sort: "price", future: [1] }, later: true } },
])("$case comes back as it was", ({ stored }) => {
  expect(rulesOf(draftOf(rules(stored)))).toEqual({ rules: stored });
});

// An edit keeps a platform preset's run-time settings; a copy, which may be an account's, leaves them out.
it.each([
  { copy: false, written: { default: {}, advanced: { order: ["g1"] } } },
  { copy: true, written: { default: {} } },
])("leaves out what writes nothing, and advanced on a copy ($copy)", ({ copy, written }) => {
  const stored = rules({
    default: { classes: ["*"], verified_only: false, max_price: {} },
    advanced: { order: ["g1"] },
  });
  expect(rulesOf(draftOf(stored, copy))).toEqual({ rules: written });
});

it.each([
  { case: "no source checked", edit: { classes: [] }, field: "classes" },
  { case: "policies that leave out may_train", edit: { policies: ["no_retention"] }, field: "policies" },
  { case: "no policy checked", edit: { policies: [] }, field: "policies" },
  { case: "a product score", edit: { scored: true }, field: "score" },
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

// Scoring is not live, so a stored score (a copy, or rules written through the API) is refused rather than kept, and
// dropping it writes the rule without one.
it.each([{ min_score: 90 }, { min_score_confidence: 0.5 }])("refuses a stored score until it is removed", (score) => {
  const draft = draftOf(rules({ default: {}, rules: [{ author: "openai", ...score }] }));
  expect(rulesOf(draft)).toEqual({ invalid: { rule: 0, field: "score" } });
  draft.overrides = draft.overrides.map((override) => ({ ...override, scored: false }));
  expect(rulesOf(draft)).toEqual({
    rules: { default: {}, rules: [{ author: "openai" }] },
  });
});

// A null score is no threshold to the server, so the rule saves, without it.
it.each([{ min_score: null }, { min_score_confidence: null }])("saves a null score as no score", (score) => {
  expect(rulesOf(draftOf(rules({ default: score })))).toEqual({ rules: { default: {} } });
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
