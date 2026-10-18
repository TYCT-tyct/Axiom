import { ApiError } from "@axiom/api";
import { expect, it } from "vitest";
import { type Attempt, failed, type Sendable } from "./attempt";

const apiError = (status: number, code?: string) =>
  new ApiError(status, null, code ? { body: { error: { code, message: code } } } : undefined);

const editing: Sendable<string> = { state: "editing", key: "k1" };
const unknown: Sendable<string> = { state: "unknown", key: "k1", request: "first" };

it.each<{ case: string; from: Sendable<string>; error: unknown; want: Attempt<string> }>([
  { case: "no answer keeps the key and the request", from: editing, error: apiError(0), want: unknown },
  { case: "a server failure keeps the key and the request", from: editing, error: apiError(503), want: unknown },
  // a proxy's page in place of the answer, after the change may have been made
  {
    case: "an answer that could not be read keeps the key and the request",
    from: editing,
    error: new Error("the response could not be read"),
    want: unknown,
  },
  {
    case: "a success without its result keeps the key and the request",
    from: editing,
    error: apiError(201),
    want: unknown,
  },
  {
    case: "a 4xx that is not the server's keeps the key and the request",
    from: editing,
    error: apiError(413),
    want: unknown,
  },
  {
    case: "a refusal frees the form under a new key",
    from: editing,
    error: apiError(422, "unprocessable"),
    want: { state: "editing", key: "k2" },
  },
  {
    case: "a stale revision stops sending",
    from: editing,
    error: apiError(409, "stale_revision"),
    want: { state: "stale" },
  },
  {
    case: "a reused key stops sending and never takes a new key",
    from: editing,
    error: apiError(409, "idempotency_key_reused"),
    want: { state: "reused" },
  },
  { case: "unknown, then no answer again", from: unknown, error: apiError(0), want: unknown },
  // after an unknown result no refusal proves the first try was not done
  { case: "unknown, then refused", from: unknown, error: apiError(422, "unprocessable"), want: unknown },
  { case: "unknown, then rate limited", from: unknown, error: apiError(429, "rate_limited"), want: unknown },
  {
    case: "unknown, then reused",
    from: unknown,
    error: apiError(409, "idempotency_key_reused"),
    want: { state: "reused" },
  },
  { case: "unknown, then stale", from: unknown, error: apiError(409, "stale_revision"), want: { state: "stale" } },
])("$case", ({ from, error, want }) => {
  const request = from.state === "unknown" ? from.request : "first";
  expect(failed(from, request, error, () => "k2")).toEqual(want);
});
