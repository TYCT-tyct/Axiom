import createClient from "openapi-fetch";
import type { paths } from "./console.gen";

// How long a call may take, body included, before it counts as having no response. A console call answers in well
// under a second; ten seconds still lets one through a slow mobile network, while a half-open connection, which would
// otherwise leave a page loading forever, shows the error within about half a minute, two retries included.
const requestTimeout = 10_000;

// The console's API (api/console.yaml): paths, parameters and bodies are typed from the contract. A call ends at the
// timeout or when the caller's signal aborts, whichever comes first; a query passes TanStack Query's signal, so a
// query nobody watches any more stops its request.
export const consoleApi = createClient<paths>({
  fetch: (request) =>
    fetch(request, { signal: AbortSignal.any([request.signal, AbortSignal.timeout(requestTimeout)]) }),
});

// ApiError is a call that did not succeed: status is the HTTP status, 0 when no response arrived, and requestId is the
// server's X-Request-ID, which finds the call in the server's log.
class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly requestId: string | null,
    options?: ErrorOptions,
  ) {
    super(status === 0 ? "no response" : `HTTP ${status}`, options);
    this.name = "ApiError";
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

// unwrap turns an openapi-fetch call into its data, or throws ApiError, so a query's error has that shape. The one
// exception is a response that arrived but could not be parsed, such as a page instead of JSON: openapi-fetch drops the
// response when parsing fails, so there is no status to report.
export async function unwrap<T>(call: Promise<{ data?: T; response: Response }>): Promise<T> {
  let result: { data?: T; response: Response };
  try {
    result = await call;
  } catch (cause) {
    if (isLostResponse(cause)) {
      throw new ApiError(0, null, { cause });
    }
    throw new Error("the response could not be read", { cause });
  }
  const { data, response } = result;
  if (!response.ok || data === undefined) {
    throw new ApiError(response.status, response.headers.get("X-Request-ID"));
  }
  return data;
}

// isTransient tells whether a failed call may succeed when sent again: no response arrived, or the server failed. A
// refused request (4xx) and a response that could not be read fail the same way again.
export function isTransient(error: unknown) {
  return error instanceof ApiError && (error.status === 0 || error.status >= 500);
}
