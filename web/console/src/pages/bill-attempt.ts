import { accountKey } from "@axiom/app";
import { skipToken, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useMemo } from "react";

type State = { attempt: { state: string }; pending: boolean };

function owns(state: State, key: string) {
  return "key" in state.attempt && state.attempt.key === key;
}

// Route components share one attempt for each account and operation. Account queries are cleared at session changes.
export function useBillAttempt<S extends State>(scope: string, operation: string, initialData: () => S) {
  const client = useQueryClient();
  const queryKey = useMemo(() => [accountKey, "bill-attempt", scope, operation], [scope, operation]);
  const { data: state } = useQuery({ queryKey, queryFn: skipToken, initialData, gcTime: Infinity });
  const change = useCallback(
    (apply: (current: S) => S | undefined) => {
      let changed = false;
      client.setQueryData<S>(queryKey, (current) => {
        if (!current) return undefined;
        const next = apply(current);
        changed = next !== undefined;
        return next;
      });
      return changed;
    },
    [client, queryKey],
  );
  const start = (key: string, apply: (current: S) => S) =>
    change((current) => {
      if (current.pending || !owns(current, key)) return undefined;
      return { ...apply(current), pending: true };
    });
  const finish = (key: string, apply: (current: S) => S) =>
    change((current) => {
      if (!current.pending || !owns(current, key)) return undefined;
      return { ...apply(current), pending: false };
    });

  // The disabled query starts with S and all writes replace it with another S.
  return { state: state as S, change, start, finish };
}
