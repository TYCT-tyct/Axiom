import { afterEach, describe, expect, it, vi } from "vitest";
import { consoleApi, isTransient, unwrap } from "./index";

function respond(status: number, data?: unknown) {
  return Promise.resolve({ data, response: new Response(null, { status, headers: { "X-Request-ID": "req-1" } }) });
}

async function failure(call: Promise<{ data?: unknown; response: Response }>) {
  return unwrap(call).then(
    () => expect.unreachable("the call succeeded"),
    (error: unknown) => error,
  );
}

describe("unwrap", () => {
  it("returns the data of a successful call", async () => {
    await expect(unwrap(respond(200, { version: "v1" }))).resolves.toEqual({ version: "v1" });
  });

  it.each([
    { name: "a refused call", call: () => respond(403), status: 403 },
    { name: "a server failure", call: () => respond(500), status: 500 },
    { name: "a successful call without a body", call: () => respond(200), status: 200 },
  ])("reports $name with its status and request ID", async ({ call, status }) => {
    expect(await failure(call())).toMatchObject({ status, requestId: "req-1" });
  });

  it.each([
    { name: "the network failed", cause: new TypeError("Failed to fetch") },
    { name: "the call was aborted", cause: new DOMException("aborted", "AbortError") },
    { name: "the call timed out", cause: new DOMException("timed out", "TimeoutError") },
  ])("reports no response when $name, keeping the cause", async ({ cause }) => {
    expect(await failure(Promise.reject(cause))).toMatchObject({ status: 0, requestId: null, cause });
  });

  it("tells a response that could not be read from no response, keeping the cause", async () => {
    const cause = new SyntaxError("Unexpected token '<'");
    const error = await failure(Promise.reject(cause));
    expect(error).toMatchObject({ cause });
    expect(error).not.toHaveProperty("status");
  });
});

describe("isTransient", () => {
  const unreadable = () => failure(Promise.reject(new SyntaxError("Unexpected token '<'")));
  it.each([
    { name: "no response", error: () => failure(Promise.reject(new TypeError("Failed to fetch"))), want: true },
    { name: "a server failure", error: () => failure(respond(503)), want: true },
    { name: "a refused call", error: () => failure(respond(403)), want: false },
    { name: "a response that could not be read", error: unreadable, want: false },
    { name: "an error from elsewhere", error: async () => new Error("a bug"), want: false },
  ])("$name → $want", async ({ error, want }) => {
    expect(isTransient(await error())).toBe(want);
  });
});

describe("consoleApi", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("stops a call when the caller's signal aborts", async () => {
    // A server that never answers: only the signal ends the call.
    vi.stubGlobal("fetch", (_: Request, init?: RequestInit) => {
      const signal = init?.signal;
      return new Promise<Response>((_resolve, reject) => {
        signal?.addEventListener("abort", () => reject(signal.reason));
      });
    });
    const caller = new AbortController();
    const call = consoleApi.GET("/api/console/v1/version", { baseUrl: "http://localhost", signal: caller.signal });
    caller.abort();
    expect(await failure(call)).toMatchObject({ status: 0 });
  });
});
