import {
  type LoginLinkProps,
  type LoginNavigateOptions,
  type LoginNavigation,
  LoginNavigationProvider,
  type LoginPath,
  type LoginSearch,
} from "@axiom/login";
import { Link, useRouter, useRouterState } from "@tanstack/react-router";
import { type ReactNode, useCallback, useMemo } from "react";

// Renders a real <a href> via TanStack Router's Link, passing validated guest search parameters and dropping invite
// when moving between sign-in pages (router.tsx guestSearch).
function ConsoleLoginLink({ to, search, ...props }: LoginLinkProps) {
  return <Link to={to} search={{ next: search?.next, invite: search?.invite }} {...props} />;
}

function readStringParam(search: unknown, key: keyof LoginSearch): string | undefined {
  const value = (search as Record<string, unknown> | undefined)?.[key];
  return typeof value === "string" ? value : undefined;
}

// ConsoleLoginNavigation adapts the console's TanStack Router for @axiom/login's pages. Reading search from the
// deepest active match ensures guestSearch's validation (safeNext) applies even when mounted at the login layout route.
export function ConsoleLoginNavigation({ children }: { children: ReactNode }) {
  const router = useRouter();
  const next = useRouterState({ select: (state) => readStringParam(state.matches.at(-1)?.search, "next") });
  const invite = useRouterState({ select: (state) => readStringParam(state.matches.at(-1)?.search, "invite") });
  const oauth = useRouterState({ select: (state) => readStringParam(state.matches.at(-1)?.search, "oauth") });
  const navigate = useCallback(
    (to: LoginPath, options?: LoginNavigateOptions) => {
      void router.navigate({
        to,
        search: options?.search
          ? (prev: Record<string, unknown>) => ({
              ...prev,
              ...options.search,
              next: options.search?.next,
              invite: options.search?.invite,
            })
          : undefined,
        replace: options?.replace,
      });
    },
    [router],
  );
  const value = useMemo<LoginNavigation>(() => {
    const search: LoginSearch = { next, invite, oauth };
    return { search, navigate, Link: ConsoleLoginLink };
  }, [next, invite, oauth, navigate]);
  return <LoginNavigationProvider value={value}>{children}</LoginNavigationProvider>;
}
