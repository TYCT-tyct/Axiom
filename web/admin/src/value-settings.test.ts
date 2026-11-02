import { expect, it } from "vitest";
import { readValues, type ValueField } from "./value-settings";

const fields: ValueField[] = [{ key: "tokens", label: "Tokens", hint: "", min: 1, max: 10, format: String }];

it("treats an empty field as absent, so the server uses the default", () => {
  expect(readValues(fields, {}, "invalid")).toEqual({ tokens: undefined });
  expect(readValues(fields, { tokens: "  " }, "invalid")).toEqual({ tokens: undefined });
});

it("sends whole numbers within the bounds and refuses the rest", () => {
  expect(readValues(fields, { tokens: "5" }, "invalid")).toEqual({ tokens: 5 });
  expect(readValues(fields, { tokens: "0" }, "invalid")).toEqual({ problem: "invalid" });
  expect(readValues(fields, { tokens: "11" }, "invalid")).toEqual({ problem: "invalid" });
  expect(readValues(fields, { tokens: "1.5" }, "invalid")).toEqual({ problem: "invalid" });
  expect(readValues(fields, { tokens: "one" }, "invalid")).toEqual({ problem: "invalid" });
});
