import { createRootRoute, createRoute, lazyRouteComponent, Outlet, redirect } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { Layout } from "../layout";

// What the admin's route files share: one file per group of the navigation (nav.ts), which router.tsx assembles.

export const root = createRootRoute({ component: Outlet });

export const app = createRoute({ getParentRoute: () => root, id: "app", component: Layout });

// A page of the shell, its own chunk, loaded when the user is about to open it.
export const shellPage = <S extends Record<string, unknown> = Record<never, never>>(
  path: string,
  load: () => Promise<{ default: () => ReactNode }>,
  validateSearch?: (search: Record<string, unknown>) => S,
  beforeLoad?: (search: S) => void,
) =>
  createRoute({
    getParentRoute: () => app,
    path,
    validateSearch,
    beforeLoad: beforeLoad && (({ search }) => beforeLoad(search as S)),
    component: lazyRouteComponent(load),
  });

// A page that has moved: its old address takes the visitor to where it is now, and is never shown. An address the design
// folded into a tab of a page keeps what it was asked for (its search), so a link, a bookmark and the dashboard's
// drill-downs still arrive.
type Moved = { to: string; search?: Record<string, unknown> };
export const movedPage = <S extends Record<string, unknown> = Record<never, never>>(
  path: string,
  to: (params: Record<string, string | undefined>, search: S) => Moved,
  validateSearch?: (search: Record<string, unknown>) => S,
) =>
  createRoute({
    getParentRoute: () => app,
    path,
    validateSearch,
    beforeLoad: ({ params, search }) => {
      throw redirect({ ...to(params, search as S), replace: true } as never);
    },
  });

// A filter in the address is text, or nothing.
export const text = (value: unknown) => (typeof value === "string" && value.trim() !== "" ? value : undefined);
