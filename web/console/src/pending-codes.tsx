import { consoleApi, unwrap } from "@axiom/api";
import {
  currentSession,
  holdsSession,
  identityOf,
  sessionBinding,
  useCachedSession,
  useShowRecoveryCodes,
} from "@axiom/app";
import { useQueryClient } from "@tanstack/react-query";
import { createContext, type ReactNode, use, useCallback, useEffect, useRef, useState } from "react";
import { forgetPending, takePending } from "./pending-codes-store";

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
// session that asked is still the current one, and is dropped when that session ends.
export function PendingCodesProvider({ children }: { children: ReactNode }) {
  const client = useQueryClient();
  const show = useShowRecoveryCodes();
  const binding = sessionBinding(useCachedSession());
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
    if (!binding) {
      setPending(false);
      return;
    }
    const sent = binding;
    setPending(true);
    void takePending(sent, attempt, () => unwrap(consoleApi.POST("/api/console/v1/auth/recovery-codes/take"))).then(
      (result) => {
        const now = currentSession(client);
        if (!now || !holdsSession(client, sent)) {
          return;
        }
        setPending(false);
        if ("failure" in result) {
          setFailure(result.failure);
          return;
        }
        if (result.codes.length > 0) {
          show({ csrf_token: now.csrf_token, recovery_codes: result.codes }, identityOf(now));
          setFailure(null);
        }
      },
    );
  }, [attempt, binding, client, show]);

  const retry = useCallback(() => setAttempt((count) => count + 1), []);
  return <Pending value={{ failure, pending, retry }}>{children}</Pending>;
}
