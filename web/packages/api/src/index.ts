import createClient from "openapi-fetch";
import type { components as adminComponents, paths as adminPaths } from "./admin.gen";
import type { components, paths } from "./console.gen";

export type Schemas = components["schemas"];
export type AdminSchemas = adminComponents["schemas"];
export type ErrorCode = Schemas["ErrorCode"];

// How long a call may take, body included, before it counts as having no response. A console call answers in well
// under a second; ten seconds still lets one through a slow mobile network, while a half-open connection, which would
// otherwise leave a page loading forever, shows the error within about half a minute, two retries included.
const requestTimeout = 10_000;

// The session's CSRF token, which the page keeps in memory only (the session cookie is HttpOnly). Every request that
// changes data sends it with the content type the contract requires; the browser adds the Origin itself. An app is one
// bundle talking to one surface, so one token serves its client.
let csrfToken = "";

export function setCsrfToken(token: string) {
  csrfToken = token;
}

// fetchWithin is fetch that ends a call after ms, or when the caller's signal aborts, whichever comes first. A call
// known to take longer than requestTimeout passes its own as openapi-fetch's per-request `fetch`.
export const fetchWithin = (ms: number) => (request: Request) =>
  fetch(request, { signal: AbortSignal.any([request.signal, AbortSignal.timeout(ms)]) });

// A client for one contract: paths, parameters and bodies are typed from it. A call ends at the timeout or when the
// caller's signal aborts; a query passes TanStack Query's signal, so a query nobody watches any more stops its request.
function createApi<P extends object>() {
  const api = createClient<P>({ fetch: fetchWithin(requestTimeout) });
  api.use({
    onRequest({ request }) {
      if (request.method !== "GET" && request.method !== "HEAD") {
        request.headers.set("Content-Type", "application/json");
        if (csrfToken) {
          request.headers.set("X-CSRF-Token", csrfToken);
        }
      }
      return request;
    },
  });
  return api;
}

// The console's API (api/console.yaml) and the admin's (api/admin.yaml).
export const consoleApi = createApi<paths>();
export const adminApi = createApi<adminPaths>();

// What a failed call says in its body (the contract's Error), if it has one; a gateway's page or an empty body has none.
function errorDetail(body: unknown): Schemas["ErrorDetail"] | undefined {
  const detail = (body as { error?: Partial<Schemas["ErrorDetail"]> } | undefined)?.error;
  return typeof detail?.code === "string" ? (detail as Schemas["ErrorDetail"]) : undefined;
}

// ApiError is a call that did not succeed: status is the HTTP status, 0 when no response arrived, and requestId is the
// server's X-Request-ID, which finds the call in the server's log. code is what the server says went wrong (a page
// translates it), fields which parts of the request broke a constraint, and retryAfter the seconds a limit asks to wait.
export class ApiError extends Error {
  readonly code: ErrorCode | undefined;
  readonly fields: Schemas["ErrorField"][];
  readonly retryAfter: number | undefined;

  constructor(
    readonly status: number,
    readonly requestId: string | null,
    options?: ErrorOptions & { body?: unknown; retryAfter?: number },
  ) {
    super(status === 0 ? "no response" : `HTTP ${status}`, options);
    this.name = "ApiError";
    const detail = errorDetail(options?.body);
    this.code = detail?.code;
    this.fields = detail?.fields ?? [];
    this.retryAfter = options?.retryAfter;
  }
}

// fetch rejects with a TypeError when the network fails and with the signal's reason when the call is aborted or times
// out; reading a body that stops arriving fails the same ways.
function isLostResponse(error: unknown) {
  return (
    error instanceof TypeError ||
    (error instanceof DOMException && (error.name === "AbortError" || error.name === "TimeoutError"))
  );
}

type Settled<T> = { data?: T; error?: unknown; response: Response };

// settle waits for a call and throws ApiError unless it succeeded. The one exception is a response that arrived but
// could not be parsed, such as a page instead of JSON: openapi-fetch drops the response when parsing fails, so there is
// no status to report.
async function settle<T>(call: Promise<Settled<T>>): Promise<Settled<T>> {
  let result: Settled<T>;
  try {
    result = await call;
  } catch (cause) {
    if (isLostResponse(cause)) {
      throw new ApiError(0, null, { cause });
    }
    throw new Error("the response could not be read", { cause });
  }
  const { error, response } = result;
  if (!response.ok) {
    const retryAfter = Number(response.headers.get("Retry-After")) || undefined;
    throw new ApiError(response.status, response.headers.get("X-Request-ID"), { body: error, retryAfter });
  }
  return result;
}

// unwrapChange turns an openapi-fetch call into its data, or throws ApiError, so a query's error has that shape. It
// also says whether the answer is the stored result of an earlier request with the same Idempotency-Key (admin API
// operations that change data), in which case nothing was done again.
export async function unwrapChange<T>(call: Promise<Settled<T>>): Promise<{ data: T; replayed: boolean }> {
  const { data, response } = await settle(call);
  if (data === undefined) {
    throw new ApiError(response.status, response.headers.get("X-Request-ID"));
  }
  return { data, replayed: response.headers.get("Idempotency-Replayed") === "true" };
}

// unwrap is unwrapChange for a read, which has no replay to tell.
export async function unwrap<T>(call: Promise<Settled<T>>): Promise<T> {
  return (await unwrapChange(call)).data;
}

// unwrapEmpty is unwrap for an operation that answers 204.
export async function unwrapEmpty(call: Promise<Settled<unknown>>) {
  await settle(call);
}

// unwrapEmptyChange is unwrapChange for a change that answers 204: no data, but still the replay flag.
export async function unwrapEmptyChange(call: Promise<Settled<unknown>>) {
  const { response } = await settle(call);
  return { data: null, replayed: response.headers.get("Idempotency-Replayed") === "true" };
}

// isTransient tells whether a failed call may succeed when sent again: no response arrived, or the server failed. A
// refused request (4xx) and a response that could not be read fail the same way again.
export function isTransient(error: unknown) {
  return error instanceof ApiError && (error.status === 0 || error.status >= 500);
}
