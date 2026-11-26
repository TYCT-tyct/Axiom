import { expect, it } from "vitest";
import { eventLabel } from "./audit-events";
import en from "./locales/en/audit-events";
import zh from "./locales/zh/audit-events";

it.each([
  { action: "security.sign_in", expected: { text: "登录成功" } },
  { action: "supplier.endpoint.enabled", expected: { text: "启用或停用端点" } },
  { action: "something.new", expected: { text: "其他操作", raw: "something.new" } },
  { action: "constructor", expected: { text: "其他操作", raw: "constructor" } },
  { action: undefined, expected: { text: "—" } },
])("names $action", ({ action, expected }) => {
  expect(eventLabel(zh, "其他操作", action)).toEqual(expected);
});

it("has the same events in both languages, none left empty", () => {
  expect(Object.keys(en).sort()).toEqual(Object.keys(zh).sort());
  expect([...Object.values(zh), ...Object.values(en)].every((text) => text.trim() !== "")).toBe(true);
});
