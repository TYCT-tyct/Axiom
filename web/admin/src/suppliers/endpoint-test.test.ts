import type { AdminSchemas } from "@axiom/api";
import { expect, it } from "vitest";
import { duration, endpointState, testFormats } from "./endpoint-test";

const endpoint = (over: Partial<AdminSchemas["SupplierEndpoint"]>): AdminSchemas["SupplierEndpoint"] => ({
  id: "e",
  group_id: "g",
  model_id: "m",
  upstream_name: "model",
  formats: ["chat"],
  cost_state: "known",
  enabled: true,
  ...over,
});

it.each([
  { case: "enabled with a known cost", over: {}, want: "ready" },
  { case: "switched off, whatever its cost", over: { enabled: false }, want: "off" },
  { case: "with no known cost", over: { cost_state: "unknown" as const }, want: "unknown" },
  { case: "with conflicting costs", over: { cost_state: "conflict" as const }, want: "conflict" },
])("an endpoint $case is $want", ({ over, want }) => {
  expect(endpointState(endpoint(over))).toBe(want);
});

it.each([
  { formats: ["chat", "embeddings"] as const, want: ["chat"] },
  { formats: ["messages", "responses", "rerank"] as const, want: ["messages", "responses"] },
  { formats: ["embeddings", "rerank"] as const, want: [] },
])("an endpoint of $formats can be tested in $want", ({ formats, want }) => {
  expect(testFormats([...formats])).toEqual(want);
});

it.each([
  { ms: 0, want: { unit: "ms", value: 0 } },
  { ms: 999, want: { unit: "ms", value: 999 } },
  { ms: 1000, want: { unit: "s", value: 1 } },
  { ms: 12_345, want: { unit: "s", value: 12.3 } },
  { ms: 44_960, want: { unit: "s", value: 45 } },
])("$ms ms reads as $want.value $want.unit", ({ ms, want }) => {
  expect(duration(ms)).toEqual(want);
});
