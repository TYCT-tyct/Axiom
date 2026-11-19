import { useCallback, useEffect, useRef, useState } from "react";

// Every way in a sign-in page offers (a password, a provider, a passkey, a two-step code) is an attempt, and a page has
// one at a time: while one is out, the others wait. An attempt ends when it settles, when the visitor leaves the page,
// or when the browser shows the page again from its back-forward cache, and what it was still doing is cancelled.
//
// Invariant: a result that comes for an ended attempt changes nothing on the page, with one exception. Once a request
// that issues a session has been sent, the server may have set its cookie, so its answer is never dropped: the caller
// adopts the session it names (useStartSession), and the page follows the session, never a cookie of its own.

export type Attempt = {
  // aborts what the attempt is still asking for once it has ended
  readonly signal: AbortSignal;
  // whether this attempt is still the page's: false once it has ended, and before sending anything that would sign in
  live: () => boolean;
  // done ends the attempt that succeeded, or that handed the visitor to another page (a provider's)
  done: () => void;
  // fail ends the attempt and keeps why, to show; for an ended attempt it does nothing
  fail: (error: unknown) => void;
};

export type Attempts = {
  // what is out, if anything: the page disables every other way in meanwhile
  readonly kind: string | null;
  // the latest attempt's failure, which replaces any earlier one
  readonly failure: { kind: string; error: unknown } | null;
  begin: (kind: string) => Attempt | null;
};

export function useAttempts(): Attempts {
  const [state, setState] = useState<Pick<Attempts, "kind" | "failure">>({ kind: null, failure: null });
  const current = useRef<AbortController | null>(null);
  const end = useCallback((failure: Attempts["failure"]) => {
    current.current?.abort();
    current.current = null;
    setState((previous) => ({ kind: null, failure: failure ?? previous.failure }));
  }, []);
  useEffect(() => {
    // a page the browser brings back from its back-forward cache shows the attempt that took the visitor away, still
    // out: the visitor came back, so it is over
    const restored = (event: PageTransitionEvent) => event.persisted && end(null);
    window.addEventListener("pageshow", restored);
    return () => {
      window.removeEventListener("pageshow", restored);
      current.current?.abort();
      current.current = null;
    };
  }, [end]);
  const begin = useCallback(
    (kind: string): Attempt | null => {
      if (current.current) return null;
      const own = new AbortController();
      current.current = own;
      setState({ kind, failure: null });
      const live = () => current.current === own;
      return {
        signal: own.signal,
        live,
        done: () => live() && end(null),
        fail: (error) => live() && end({ kind, error }),
      };
    },
    [end],
  );
  return { ...state, begin };
}
