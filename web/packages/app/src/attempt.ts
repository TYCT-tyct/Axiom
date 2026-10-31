import { ApiError } from "@axiom/api";

// Where a change dialog stands with the one change it is for, which decides what its button does next. The server does
// a change at most once per Idempotency-Key, and only for the request first sent with it: the same key with another
// body is refused as reused (internal/platform/README.md「幂等」), and a new key is a new change.
export type Attempt<Request> =
  // Nothing was done with this key: the form is the person's to edit, and the button sends what it holds.
  | { state: "editing"; key: string }
  // A request went out and no answer said whether it was done. Only that request with that key may follow, so sending
  // it again cannot do it twice; the form is locked.
  | { state: "unknown"; key: string; request: Request }
  // The change was made against something that has changed since: nothing is sent until the form is filled again from
  // what is current.
  | { state: "stale" }
  // The server holds the key for another request, so the first one may have been done: nothing more is sent.
  | { state: "reused" };

export type Sendable<Request> = Extract<Attempt<Request>, { key: string }>;

export const opened = <Request>(key: string): Attempt<Request> => ({ state: "editing", key });

// failed says where a dialog stands after the request it sent from `from` failed with error.
export function failed<Request>(
  from: Sendable<Request>,
  request: Request,
  error: unknown,
  newKey: () => string,
): Attempt<Request> {
  const code = error instanceof ApiError ? error.code : undefined;
  if (code === "idempotency_key_reused") {
    return { state: "reused" };
  }
  // A stale revision is checked after the key: had an earlier try with this key been done, its result would have been
  // replayed instead. So nothing was done, and nothing can be until the person sees what is current.
  if (code === "stale_revision") {
    return { state: "stale" };
  }
  // Once the result is unknown, no other answer proves the first try was not done (a rate limit or a lost session is
  // given before the server looks at the key), so the request stays the one to send until it succeeds.
  if (from.state === "unknown" || !refused(error)) {
    return { state: "unknown", key: from.key, request };
  }
  // Refused before anything was done: the person may change the request, which makes it another change with its own key.
  return opened(newKey());
}

// failedFrom is a mutation's onError for an attempt it sends: the error is shown and the attempt is recorded where the
// failure left it, so a reload or a reopened dialog resumes the same request instead of making a second change.
export function failedFrom<Request>(change: (attempt: Attempt<Request>) => void, setFailure: (error: unknown) => void) {
  return (error: unknown, sent: { from: Sendable<Request>; request: Request }) => {
    setFailure(error);
    change(failed(sent.from, sent.request, error, () => crypto.randomUUID()));
  };
}

// Only the server's own refusal, a 4xx with its error body, proves the change was not done. No answer, a server
// failure, a response that could not be read (a proxy's page after the change was made) and a success without its
// result all leave it possible that it was.
function refused(error: unknown) {
  return error instanceof ApiError && error.status >= 400 && error.status < 500 && error.code !== undefined;
}
