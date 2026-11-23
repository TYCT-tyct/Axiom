import { useIsFetching } from "@tanstack/react-query";
import { type AnyRouter, useRouterState } from "@tanstack/react-router";
import { type ReactNode, useCallback, useMemo } from "react";
import { type SessionNavigation, SessionNavigationProvider } from "./index";

// TanStackSessionNavigation adapts a TanStack Router app (console, admin) for Gate without coupling @axiom/app's main
// entry to TanStack Router. Reading `next` from the deepest active match preserves per-route search validation
// (guestSearch) while leaving protected routes' own query parameters untouched.
export function TanStackSessionNavigation({ router, children }: { router: AnyRouter; children: ReactNode }) {
  const here = useRouterState({
    router,
    select: (state) => (state.resolvedLocation ?? state.location).href,
  });
  const next = useRouterState({
    router,
    select: (state) => {
      const value = (state.matches.at(-1)?.search as { next?: unknown } | undefined)?.next;
      return typeof value === "string" ? value : undefined;
    },
  });
  const replace = useCallback(
    (path: string) => {
      router.history.replace((router.basepath ?? import.meta.env.BASE_URL).replace(/\/$/, "") + path);
    },
    [router],
  );
  const value = useMemo<SessionNavigation>(() => ({ here, next, replace }), [here, next, replace]);
  return <SessionNavigationProvider value={value}>{children}</SessionNavigationProvider>;
}

// useWaiting tells whether the page is waiting for something the visitor asked for: the router bringing in a page, or a
// query a page shows that has no answer yet (a filter's new list, a call's details), whether the page shows the answer
// before it meanwhile or a placeholder. A query asked for ahead of its page has no observer yet, and one refreshing an
// answer already shown has data: neither is waited for.
export function useWaiting() {
  const routing = useRouterState({ select: (state) => state.status === "pending" });
  const asking = useIsFetching({
    predicate: (query) => query.state.data === undefined && query.getObserversCount() > 0,
  });
  return routing || asking > 0;
}
