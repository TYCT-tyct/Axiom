import { consoleApi, unwrap } from "@axiom/api";
import {
  currentSession,
  holdsSession,
  identityOf,
  sessionBinding,
  useCachedSession,
  useHideRecoveryCodes,
  useShowRecoveryCodes,
} from "@axiom/app";
import { useQueryClient } from "@tanstack/react-query";
import { useRouterState } from "@tanstack/react-router";
import { createContext, type ReactNode, use, useCallback, useEffect, useRef, useState } from "react";
import { forgetPending, spendPending, takePending } from "./pending-codes-store";
import type { router as consoleRouter } from "./router";

// The state of the take for the current session: the reason it failed, whether another try is out, and how to make one.
export type PendingCodes = { failure: unknown; pending: boolean; retry: () => void };

const Pending = createContext<PendingCodes | null>(null);

// usePendingCodes is the take's state, or null outside the console's provider.
export function usePendingCodes(): PendingCodes | null {
  return use(Pending);
}

// PendingCodesProvider owns the take for the whole console, above the routes. Its answer is the only copy of the codes
// (the server cleared them when it answered), so a page left while the request is out must not drop it; the store reuses
// the request for the session, which also covers StrictMode running the effect twice. A result is shown only while the
// session that asked is still the current one, and is dropped when that session ends; once with the dialog, the store
// keeps only the mark that it was taken. The pages a mailed link opens have no gate (auth/reset-password.tsx,
// auth/verify-email.tsx): the session they read may be another person's than the visitor's, so while one is open no take
// runs and no codes are on screen, an open dialog included (useHideRecoveryCodes); an answer that lands then is shown
// once another page is.
export function PendingCodesProvider({ router, children }: { router: typeof consoleRouter; children: ReactNode }) {
  const client = useQueryClient();
  const show = useShowRecoveryCodes();
  const binding = sessionBinding(useCachedSession());
  const away = useRouterState({
    router,
    select: (state) =>
      // by the pages' paths, which stay the same whatever frame the pages stand in
      state.matches.some(({ fullPath }) => fullPath === "/reset-password" || fullPath === "/verify-email"),
  });
  useHideRecoveryCodes(away);
  const [failure, setFailure] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const previous = useRef("");

  // A result belongs to the person and session that asked for it: when either changes, it is forgotten, not shown.
  useEffect(() => {
    if (previous.current && previous.current !== binding) {
      forgetPending(previous.current);
    }
    previous.current = binding;
    setFailure(null);
  }, [binding]);

  useEffect(() => {
    if (!binding || away) {
      setPending(false);
      return;
    }
    const sent = binding;
    let live = true;
    setPending(true);
    void takePending(sent, attempt, () => unwrap(consoleApi.POST("/api/console/v1/auth/recovery-codes/take"))).then(
      (result) => {
        const now = currentSession(client);
        if (!live || !now || !holdsSession(client, sent)) {
          return;
        }
        setPending(false);
        if ("failure" in result) {
          setFailure(result.failure);
          return;
        }
        if (result.codes.length > 0) {
          show({ csrf_token: now.csrf_token, recovery_codes: result.codes }, identityOf(now));
          spendPending(sent, attempt);
          setFailure(null);
        }
      },
    );
    return () => {
      live = false;
    };
  }, [attempt, binding, client, show, away]);

  const retry = useCallback(() => setAttempt((count) => count + 1), []);
  return <Pending value={{ failure, pending, retry }}>{children}</Pending>;
}
