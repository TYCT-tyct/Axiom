// Changes whose first request got no answer that says whether it was done (attempt.ts), kept past their dialog and past
// a sign-in: while one is held, its dialog offers that request again or giving it up, never a new key, because the
// server does a change once per key and a new key would do it twice.
//
// The request is held as the function that sends it, in memory. Storage in the browser (sessionStorage) would need the
// body written as text, and some bodies hold what must not outlive the dialog in readable form (a supplier's login);
// the function holds the same body without that. What this does not survive is the page being reloaded or the tab
// closed, after which a change is made afresh with a new key.
//
// The scope names the account's session and the change (ChangeDialog's operation), so another person who signs in on
// this browser, or another thing changed, never meets it.
export type Unsettled = { key: string; request: () => Promise<unknown> };

const held = new Map<string, Unsettled>();

export const unsettled = {
  get: (scope: string) => held.get(scope),
  hold: (scope: string, change: Unsettled) => void held.set(scope, change),
  drop: (scope: string) => void held.delete(scope),
};
