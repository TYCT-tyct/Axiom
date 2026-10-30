import { safeNext } from "@axiom/app";
import { focusPage, NotFoundState, routerDefaults } from "@axiom/ui";
import { createRootRoute, createRoute, createRouter, lazyRouteComponent, Outlet } from "@tanstack/react-router";
import { Layout } from "./layout";

const root = createRootRoute({ component: Outlet });

// The sign-in pages sit outside the shell. They remember where the visitor was going, to take them back after signing
// in, and a registration link can carry the invite code.
const guestSearch = (search: Record<string, unknown>) => ({
  next: safeNext(search.next),
  invite: typeof search.invite === "string" ? search.invite : undefined,
});

const app = createRoute({ getParentRoute: () => root, id: "app", component: Layout });

// Every page is its own chunk, loaded when the user is about to open it; the first screen loads the page of the address
// it opens (scripts/budget.ts counts it).
const routeTree = root.addChildren([
  createRoute({
    getParentRoute: () => root,
    path: "/sign-in",
    validateSearch: guestSearch,
    component: lazyRouteComponent(() => import("./auth/sign-in")),
  }),
  createRoute({
    getParentRoute: () => root,
    path: "/mfa",
    validateSearch: guestSearch,
    component: lazyRouteComponent(() => import("./auth/mfa")),
  }),
  createRoute({
    getParentRoute: () => root,
    path: "/register",
    validateSearch: guestSearch,
    component: lazyRouteComponent(() => import("./auth/register")),
  }),
  createRoute({
    getParentRoute: () => root,
    path: "/verify-email",
    component: lazyRouteComponent(() => import("./auth/verify-email")),
  }),
  app.addChildren([
    createRoute({
      getParentRoute: () => app,
      path: "/",
      component: lazyRouteComponent(() => import("./pages/overview")),
    }),
    createRoute({
      getParentRoute: () => app,
      path: "/keys",
      validateSearch: (search: Record<string, unknown>) => ({
        name: text(search.name),
        prefix: text(search.prefix),
        status: keyStatus(search.status),
        created_after: text(search.created_after),
        created_before: text(search.created_before),
      }),
      component: lazyRouteComponent(() => import("./pages/keys")),
    }),
    createRoute({
      getParentRoute: () => app,
      path: "/calls",
      validateSearch: (search: Record<string, unknown>) => ({
        since: text(search.since),
        until: text(search.until),
        model: text(search.model),
        client_app: text(search.client_app),
      }),
      component: lazyRouteComponent(() => import("./pages/calls")),
    }),
    createRoute({
      getParentRoute: () => app,
      path: "/calls/$callId",
      component: lazyRouteComponent(() => import("./pages/call")),
    }),
    createRoute({
      getParentRoute: () => app,
      path: "/balance",
      component: lazyRouteComponent(() => import("./pages/balance")),
    }),
    createRoute({
      getParentRoute: () => app,
      path: "/orders",
      component: lazyRouteComponent(() => import("./pages/orders")),
    }),
    createRoute({
      getParentRoute: () => app,
      path: "/statement",
      component: lazyRouteComponent(() => import("./pages/statement")),
    }),
    createRoute({
      getParentRoute: () => app,
      path: "/security",
      component: lazyRouteComponent(() => import("./pages/security")),
    }),
    createRoute({
      getParentRoute: () => app,
      path: "/notifications",
      component: lazyRouteComponent(() => import("./pages/notifications")),
    }),
    createRoute({ getParentRoute: () => app, path: "$", component: NotFoundState }),
  ]),
]);

function text(value: unknown) {
  return typeof value === "string" && value !== "" ? value : undefined;
}

// A status from the address is one the API knows; anything else is no filter, not a request it would refuse.
function keyStatus(value: unknown) {
  return value === "active" || value === "disabled" || value === "expired" ? value : undefined;
}

export const router = createRouter({ ...routerDefaults, routeTree, basepath: import.meta.env.BASE_URL });

router.subscribe("onResolved", focusPage);

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
