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
