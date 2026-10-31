import type { Attempt, Sendable } from "@axiom/app";

// An attempt kept for a form survives a reload, or the channel's return page: the key and the request go out again
// unchanged, so the server replays what was done instead of doing it twice. This store writes the whole request, so it is
// only for a form whose body holds no secret, such as a refund or a top-up: a form whose body is a credential keeps its
// unanswered key alone and asks for the credential again (web/console/src/redeem.ts). Nothing kept here is a secret, and
// a browser that refuses sessionStorage only loses it.

// readAttempt reads the attempt stored under the key, or null when there is none or it is not one a person could have
// sent: a form then starts over rather than retrying something it cannot describe.
export function readAttempt<Request>(
  store: string,
  isRequest: (value: unknown) => value is Request,
): Attempt<Request> | null {
  try {
    const raw = sessionStorage.getItem(store);
    if (!raw) {
      return null;
    }
    const value: unknown = JSON.parse(raw);
    if (typeof value !== "object" || value === null) {
      return null;
    }
    const state = (value as { state?: unknown }).state;
    if (state === "reused") {
      return { state: "reused" };
    }
    const key = (value as { key?: unknown }).key;
    if (typeof key !== "string") {
      return null;
    }
    if (state === "editing") {
      return { state: "editing", key };
    }
    const request = (value as { request?: unknown }).request;
    if (state === "unknown" && isRequest(request)) {
      return { state: "unknown", key, request };
    }
    return null;
  } catch {
    return null;
  }
}

// unanswered is the attempt of a request that went out and whose answer has not come back: only that request, under
// that key, may follow, so sending it again cannot do it a second time.
export const unanswered = <Request>(from: Sendable<Request>, request: Request): Attempt<Request> => ({
  state: "unknown",
  key: from.key,
  request,
});

// writeAttempt keeps what a reload may need: a request whose answer was unknown, and a refusal the server gave the key.
export function writeAttempt<Request>(store: string, attempt: Attempt<Request>) {
  try {
    if (attempt.state === "editing" || attempt.state === "stale") {
      sessionStorage.removeItem(store);
      return;
    }
    sessionStorage.setItem(store, JSON.stringify(attempt));
  } catch {
    // The form still locks this attempt; only a reload loses it.
  }
}

export function clearAttempt(store: string) {
  try {
    sessionStorage.removeItem(store);
  } catch {
    // nothing to clear
  }
}
