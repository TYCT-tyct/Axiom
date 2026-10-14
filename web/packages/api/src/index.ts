import createClient from "openapi-fetch";
import type { paths } from "./console.gen";

// The console's API (api/console.yaml): paths, parameters and bodies are typed from the contract.
export const consoleApi = createClient<paths>();

// ApiError is a call that did not succeed: status is the HTTP status, 0 when no response arrived, and requestId is the
// server's X-Request-ID, which finds the call in the server's log.
class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly requestId: string | null,
  ) {
    super(status === 0 ? "no response" : `HTTP ${status}`);
    this.name = "ApiError";
  }
}

// unwrap turns an openapi-fetch call into its data, or throws ApiError, so a query's error always has that shape.
export async function unwrap<T>(call: Promise<{ data?: T; response: Response }>): Promise<T> {
  let result: { data?: T; response: Response };
  try {
    result = await call;
  } catch {
    throw new ApiError(0, null);
  }
  const { data, response } = result;
  if (!response.ok || data === undefined) {
    throw new ApiError(response.status, response.headers.get("X-Request-ID"));
  }
  return data;
}
