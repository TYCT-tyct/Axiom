import { safeNext } from "@axiom/app";
import { focusHeading, modelAbilities, NotFoundState, routerDefaults } from "@axiom/ui";
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
  createRoute({
    getParentRoute: () => root,
    path: "/reset-password",
    component: lazyRouteComponent(() => import("./auth/reset-password")),
  }),
  createRoute({
    getParentRoute: () => root,
    path: "/consent",
    validateSearch: guestSearch,
    component: lazyRouteComponent(() => import("./auth/consent")),
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
        workspace_id: text(search.workspace_id),
      }),
      component: lazyRouteComponent(() => import("./pages/keys")),
    }),
    createRoute({
      getParentRoute: () => app,
      path: "/workspaces",
      component: lazyRouteComponent(() => import("./pages/workspaces")),
    }),
    createRoute({
      getParentRoute: () => app,
      path: "/routing-presets",
      component: lazyRouteComponent(() => import("./pages/routing-presets")),
    }),
    createRoute({
      getParentRoute: () => app,
      path: "/provider-keys",
      component: lazyRouteComponent(() => import("./pages/provider-keys")),
    }),
    createRoute({
      getParentRoute: () => app,
      path: "/models",
      validateSearch: (search: Record<string, unknown>) => ({
        q: text(search.q),
        author: text(search.author),
        ability: modelAbility(search.ability),
      }),
      component: lazyRouteComponent(() => import("./pages/models")),
    }),
    createRoute({
      getParentRoute: () => app,
      path: "/usage",
      validateSearch: (search: Record<string, unknown>) => ({
        range: usageRange(search.range),
        since: text(search.since),
        until: text(search.until),
        granularity: usageGranularity(search.granularity),
        metric: usageMetric(search.metric),
        group_by: usageGroupBy(search.group_by),
        currency: text(search.currency),
        model: text(search.model),
        key_id: text(search.key_id),
        workspace_id: text(search.workspace_id),
      }),
      component: lazyRouteComponent(() => import("./pages/usage")),
    }),
    createRoute({
      getParentRoute: () => app,
      path: "/calls",
      validateSearch: (search: Record<string, unknown>) => ({
        since: text(search.since),
        until: text(search.until),
        preset: callRangePreset(search.preset),
        model: text(search.model),
        client_app: text(search.client_app),
        key_id: text(search.key_id),
        workspace_id: text(search.workspace_id),
        status: callStatus(search.status),
        fell_back:
          search.fell_back === true || search.fell_back === "true" || search.fell_back === "1" ? true : undefined,
        request_id: text(search.request_id),
        group_by: callGroupBy(search.group_by),
        tab: search.tab === "sessions" ? ("sessions" as const) : undefined,
        call: text(search.call),
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
      path: "/redeem",
      component: lazyRouteComponent(() => import("./pages/redeem")),
    }),
    createRoute({
      getParentRoute: () => app,
      path: "/orders",
      component: lazyRouteComponent(() => import("./pages/orders")),
    }),
    createRoute({
      getParentRoute: () => app,
      path: "/orders/$orderId/receipt",
      component: lazyRouteComponent(() => import("./pages/receipt")),
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
    createRoute({
      getParentRoute: () => app,
      path: "/settings",
      component: lazyRouteComponent(() => import("./pages/settings")),
    }),
    createRoute({ getParentRoute: () => app, path: "$", component: NotFoundState }),
  ]),
]);

function text(value: unknown) {
  return typeof value === "string" && value !== "" ? value : undefined;
}

// A result or grouping from the address is one the API knows; anything else is no filter, not a request it would refuse.
function callStatus(value: unknown) {
  return value === "success" || value === "error" ? value : undefined;
}

function callGroupBy(value: unknown) {
  return value === "day" || value === "model" || value === "key" ? value : undefined;
}

function callRangePreset(value: unknown) {
  return value === "today" || value === "d7" || value === "d30" || value === "month" ? value : undefined;
}

function usageRange(value: unknown) {
  return value === "today" || value === "d7" || value === "d30" || value === "month" || value === "custom"
    ? value
    : undefined;
}

function usageGranularity(value: unknown) {
  return value === "hour" || value === "day" || value === "week" ? value : undefined;
}

function usageMetric(value: unknown) {
  return value === "spend" || value === "requests" || value === "tokens" ? value : undefined;
}

function usageGroupBy(value: unknown) {
  return value === "model" || value === "key" || value === "workspace" ? value : undefined;
}

// An ability from the address is one the contract names; anything else is no filter, not a request it would refuse.
function modelAbility(value: unknown) {
  return typeof value === "string" && (modelAbilities as readonly string[]).includes(value)
    ? (value as (typeof modelAbilities)[number])
    : undefined;
}

// A status from the address is one the API knows; anything else is no filter, not a request it would refuse.
function keyStatus(value: unknown) {
  return value === "active" || value === "disabled" || value === "expired" ? value : undefined;
}

export const router = createRouter({ ...routerDefaults, routeTree, basepath: import.meta.env.BASE_URL });

router.subscribe("onResolved", focusHeading);

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
