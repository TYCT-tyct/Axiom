import { useEffect, useRef, useState, useSyncExternalStore } from "react";

// A sign-in page has one request out at a time, and while it is out the page takes nothing else (LoginFrame locks
// every press, submit and link but the cancel): so no answer can arrive for a step the visitor has since left, and no
// two requests race for the same session. What a page shows of a request (its error, a step back, the stage's
// refusal) comes only from that request's own outcome, while it is still the page's.
//
// Invariant: a request that may set or clear the session cookie (a password, a code, a passkey's finish, a sign-out) is
// never cancelled once sent, and while it is out no page of the document sends another (committed below). Its answer
// re-reads the session (useStartSession, useSignOut) whatever became of the page, so the page follows the cookie. Only
// what comes before such a request can be cancelled: a provider's address and the navigation to it, a passkey's
// challenge and the browser's prompt.

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
// passes it only to what may be cancelled, and calls commit, synchronously, before it sends what may not be (a
// passkey's finish): from then on the request has no cancel, one taken earlier included. A request that is not
// cancellable is committed from the start. onFail hears the failure if the request is still the page's.
type Run = (
  kind: string,
  work: (signal: AbortSignal, commit: () => void) => Promise<unknown>,
  options?: { cancellable?: boolean; onFail?: (error: unknown) => void },
) => void;

type State = Pick<Attempt, "kind" | "cancel" | "failure" | "done">;
const idle: State = { kind: null, cancel: null, failure: null, done: null };

// The document's committed request, while it is out. A page goes with the router (back, then forward again, mounts a
// new one), but a request sent goes on until its answer and the session read after it: every page of the document is
// held by it, not only the one that sent it.
let committed: { kind: string } | null = null;
const listeners = new Set<() => void>();
const hold = (next: typeof committed) => {
  committed = next;
  for (const listener of listeners) listener();
};
const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => listeners.delete(listener);
};

type Out = { controller: AbortController; committed: boolean };

export function useAttempt(): Attempt {
  const [state, setState] = useState<State>(idle);
  const held = useSyncExternalStore(subscribe, () => committed);
  const out = useRef<Out | null>(null);
  useEffect(
    () => () => {
      // only what is still cancellable ends with the page; a committed request goes on, holding the document
      if (out.current && !out.current.committed) out.current.controller.abort();
      out.current = null;
    },
    [],
  );
  const run: Run = (kind, work, { cancellable = false, onFail } = {}) => {
    if (out.current || committed) return;
    const own: Out = { controller: new AbortController(), committed: false };
    const mark = { kind };
    out.current = own;
    // settle ends the request with its outcome, unless it is no longer the page's
    const settle = (next: State) => {
      if (out.current !== own) return false;
      out.current = null;
      setState(next);
      return true;
    };
    const commit = () => {
      if (own.committed) return;
      own.committed = true;
      hold(mark);
      setState((now) => (now.kind === kind ? { ...now, cancel: null } : now));
    };
    const cancel = () => !own.committed && settle(idle) && own.controller.abort();
    setState({ ...idle, kind, cancel: cancellable ? cancel : null });
    if (!cancellable) commit();
    work(own.controller.signal, commit)
      .finally(() => committed === mark && hold(null))
      .then(
        () => settle({ ...idle, done: kind }),
        (error: unknown) => settle({ ...idle, failure: { kind, error } }) && onFail?.(error),
      );
  };
  // a request committed from another page of the document holds this one too, with nothing of its own to show
  const kind = state.kind ?? held?.kind ?? null;
  return { ...state, kind, run, dismiss: () => setState((now) => ({ ...now, failure: null })) };
}
