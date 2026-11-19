import { useEffect, useRef, useState } from "react";

// A sign-in page has one request out at a time, and while it is out the page takes nothing else (LoginFrame locks
// every press, submit and link but the cancel): so no answer can arrive for a step the visitor has since left, and no
// two requests race for the same session. What a page shows of a request (its error, a step back, the stage's
// refusal) comes only from that request's own outcome, while it is still the page's.
//
// Invariant: a request that may set or clear the session cookie (a password, a code, a passkey's finish, a sign-out) is
// never cancelled once sent. Its answer re-reads the session (useStartSession, useSignOut) whatever became of the page,
// so the page follows the cookie. Only what comes before such a request can be cancelled: a provider's address and the
// navigation to it, a passkey's challenge and the browser's prompt.

type Failure = { kind: string; error: unknown };

export type Attempt = {
  // the kind of request out, if any
  readonly kind: string | null;
  // ends the request out and drops its outcome, where that is still possible (Run's cancellable); null otherwise
  readonly cancel: (() => void) | null;
  // the latest request's failure; a new request clears it
  readonly failure: Failure | null;
  // the kind of the latest request that succeeded, until another starts
  readonly done: string | null;
  run: Run;
  // clears the failure shown, once what it was about has changed (the refused address edited)
  dismiss: () => void;
};

// run starts work as the page's request unless one is out. The signal ends with a cancel or with the page; work
// passes it only to what may be cancelled. onFail hears the failure if the request is still the page's.
type Run = (
  kind: string,
  work: (signal: AbortSignal) => Promise<unknown>,
  options?: { cancellable?: boolean; onFail?: (error: unknown) => void },
) => void;

type State = Pick<Attempt, "kind" | "cancel" | "failure" | "done">;
const idle: State = { kind: null, cancel: null, failure: null, done: null };

export function useAttempt(): Attempt {
  const [state, setState] = useState<State>(idle);
  const out = useRef<AbortController | null>(null);
  useEffect(
    () => () => {
      out.current?.abort();
      out.current = null;
    },
    [],
  );
  const run: Run = (kind, work, { cancellable = false, onFail } = {}) => {
    if (out.current) return;
    const own = new AbortController();
    out.current = own;
    // settle ends the request with its outcome, unless it is no longer the page's
    const settle = (next: State) => {
      if (out.current !== own) return false;
      out.current = null;
      setState(next);
      return true;
    };
    const cancel = () => settle(idle) && own.abort();
    setState({ ...idle, kind, cancel: cancellable ? cancel : null });
    work(own.signal).then(
      () => settle({ ...idle, done: kind }),
      (error: unknown) => settle({ ...idle, failure: { kind, error } }) && onFail?.(error),
    );
  };
  return { ...state, run, dismiss: () => setState((now) => ({ ...now, failure: null })) };
}
