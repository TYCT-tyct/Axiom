import { takeOAuthNext } from "@axiom/login";
import { createRootRouteWithContext, createRoute, Outlet, redirect } from "@tanstack/react-router";
import { Layout } from "../layout";
import type { RouterContext } from "../warm";

// What the console's route files share: one file per area of the shell, which router.tsx assembles.

// A provider's sign-in comes back to the root when it succeeded and to the sign-in page when it failed, neither knowing
// where the visitor was going (auth/oauth-return.ts). That return is a document of its own: its first page takes what
// was kept, and any other first page drops it, so a later visit to the root is not sent anywhere. A preload is no visit.
let landing = true;
export const root = createRootRouteWithContext<RouterContext>()({
  component: Outlet,
  beforeLoad: ({ preload, location }) => {
    if (preload || !landing) return;
    landing = false;
    const next = takeOAuthNext();
    const search = location.search as { oauth?: unknown; next?: unknown };
    if (next && location.pathname === "/") throw redirect({ href: next, replace: true });
    if (next && location.pathname === "/sign-in" && search.oauth === "failed" && !search.next) {
      throw redirect({ to: "/sign-in", search: { ...search, next, invite: undefined }, replace: true });
    }
  },
});

export const app = createRoute({ getParentRoute: () => root, id: "app", component: Layout });

export function text(value: unknown) {
  return typeof value === "string" && value !== "" ? value : undefined;
}
