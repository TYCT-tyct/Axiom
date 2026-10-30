import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearAttempt, readAttempt, unanswered, writeAttempt } from "./attempt-store";

// sessionStorage as a browser has it, without a DOM: the module only reads and writes strings.
function memoryStorage() {
  const held = new Map<string, string>();
  return {
    getItem: (key: string) => held.get(key) ?? null,
    setItem: (key: string, value: string) => void held.set(key, value),
    removeItem: (key: string) => void held.delete(key),
  };
}

type Request = { method: string; credits: string };

const isRequest = (value: unknown): value is Request =>
  typeof value === "object" && value !== null && "method" in value && "credits" in value;

const sent = { state: "unknown" as const, key: "key-1", request: { method: "easypay", credits: "10" } };

beforeEach(() => vi.stubGlobal("sessionStorage", memoryStorage()));
afterEach(() => vi.unstubAllGlobals());

describe("attempt store", () => {
  it("keeps a request whose answer was unknown so the same key and request go out again", () => {
    writeAttempt("top-up", sent);
    expect(readAttempt("top-up", isRequest)).toEqual(sent);
  });

  it("keeps nothing for a request that was not sent", () => {
    writeAttempt<Request>("top-up", { state: "editing", key: "key-1" });
    expect(readAttempt("top-up", isRequest)).toBeNull();
  });

  it("reads a key the server kept for another request as reused", () => {
    writeAttempt<Request>("top-up", { state: "reused" });
    expect(readAttempt("top-up", isRequest)).toEqual({ state: "reused" });
  });

  it("ignores a stored request it cannot recognise", () => {
    sessionStorage.setItem("top-up", JSON.stringify({ state: "unknown", key: "key-1", request: {} }));
    expect(readAttempt("top-up", isRequest)).toBeNull();
  });

  it("forgets what it kept", () => {
    writeAttempt("top-up", sent);
    clearAttempt("top-up");
    expect(readAttempt("top-up", isRequest)).toBeNull();
  });

  it("marks a request as unanswered before it goes out", () => {
    expect(unanswered({ state: "editing", key: "key-2" }, sent.request)).toEqual({
      state: "unknown",
      key: "key-2",
      request: sent.request,
    });
  });
});
