import { ApiError } from "@axiom/api";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { afterFailure, readCode, readRedemption, writeRedemption } from "./redeem";

const apiError = (status: number, code?: string) =>
  new ApiError(status, null, code ? { body: { error: { code, message: code } } } : undefined);

it("reads a code as typed", () => {
  expect(readCode({ code: " AAAAA-BBBBB-CCCCC-DDDDD " })).toEqual({ code: "AAAAA-BBBBB-CCCCC-DDDDD" });
});

it.each([
  [{}, "missing"],
  [{ code: "   " }, "missing"],
  [{ code: "x".repeat(65) }, "long"],
])("refuses %o", (values, problem) => {
  expect(readCode(values)).toBe(problem);
});

// sessionStorage as a browser has it, without a DOM: the module only reads and writes strings.
function memoryStorage() {
  const held = new Map<string, string>();
  return {
    values: () => [...held.values()],
    getItem: (key: string) => held.get(key) ?? null,
    setItem: (key: string, value: string) => void held.set(key, value),
    removeItem: (key: string) => void held.delete(key),
  };
}

let storage: ReturnType<typeof memoryStorage>;

beforeEach(() => {
  storage = memoryStorage();
  vi.stubGlobal("sessionStorage", storage);
});
afterEach(() => vi.unstubAllGlobals());

it("keeps the key of an unanswered attempt and never the code", () => {
  writeRedemption("user/account", { state: "unknown", key: "key-1", request: { code: "secret-code" } });
  expect(storage.values().join("\n")).toContain("key-1");
  expect(storage.values().join("\n")).not.toContain("secret-code");
  expect(readRedemption("user/account")).toEqual({ state: "unknown", key: "key-1" });
});

it("forgets the key once an answer is known", () => {
  writeRedemption("user/account", { state: "unknown", key: "key-1" });
  writeRedemption("user/account", { state: "editing", key: "key-2" });
  expect(readRedemption("user/account")).toBeNull();
  writeRedemption("user/account", { state: "unknown", key: "key-3" });
  writeRedemption("user/account", { state: "reused" });
  expect(readRedemption("user/account")).toBeNull();
});

it("ignores a stored value that is not a key", () => {
  sessionStorage.setItem("user/account/redeem", JSON.stringify({ key: 1 }));
  expect(readRedemption("user/account")).toBeNull();
  sessionStorage.setItem("user/account/redeem", "not json");
  expect(readRedemption("user/account")).toBeNull();
});

const editing = { state: "editing" as const, key: "k1" };
const unknown = { state: "unknown" as const, key: "k1", request: { code: "first" } };
const newKey = () => "k2";

it.each([
  { case: "no answer keeps the key and the code", from: editing, error: apiError(0), want: unknown },
  { case: "a server failure keeps the key and the code", from: editing, error: apiError(503), want: unknown },
  {
    case: "a refusal after an unanswered try keeps the key",
    from: unknown,
    error: apiError(429, "rate_limited"),
    want: unknown,
  },
  {
    case: "a refusal before anything was done starts a new key",
    from: editing,
    error: apiError(422, "invalid_redeem_code"),
    want: { state: "editing", key: "k2" },
  },
  {
    case: "a key the server holds for another code stops the form",
    from: editing,
    error: apiError(409, "idempotency_key_reused"),
    want: { state: "reused" },
  },
])("after a failure, $case", ({ from, error, want }) => {
  expect(afterFailure(from, { code: "first" }, error, newKey)).toEqual(want);
});
