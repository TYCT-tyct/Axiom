import { safeNext } from "@axiom/app";

// Where a visitor who signs in through a provider was going. The provider's callback sends the browser to the console's
// root, or back to the sign-in page when it failed (cmd/axiom CompleteOAuth), neither of which can carry it, so this tab
// keeps it across the round trip; the document the provider sends back takes it (router.tsx), and a cancelled start
// forgets it.
const key = "axiom:oauth-next";

// A round trip through a provider takes minutes; a page kept longer belongs to a trip the visitor left.
const keepFor = 15 * 60_000;

export function keepOAuthNext(next: string | undefined) {
  try {
    if (next) sessionStorage.setItem(key, JSON.stringify({ next, at: Date.now() }));
    else sessionStorage.removeItem(key);
  } catch {
    // storage refused (a private window's quota): the visitor lands on the overview instead
  }
}

export function takeOAuthNext(): string | undefined {
  try {
    const kept = sessionStorage.getItem(key);
    sessionStorage.removeItem(key);
    const { next, at } = JSON.parse(kept ?? "{}") as { next?: unknown; at?: number };
    return at !== undefined && Date.now() - at < keepFor ? safeNext(next) : undefined;
  } catch {
    return undefined;
  }
}
