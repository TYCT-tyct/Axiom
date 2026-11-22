import { safeNext } from "@axiom/app";
import { dropMailLink, keepMailLink, type MailRoute, takeOAuthNext, useLoginLinkFailed } from "@axiom/login";
import { focusHeading, modelAbilities, NotFoundState, PageFailedState, routerDefaults } from "@axiom/ui";
import {
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
  Outlet,
  type ParsedLocation,
  redirect,
} from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Layout } from "./layout";

// A provider's sign-in comes back to the root when it succeeded and to the sign-in page when it failed, neither knowing
// where the visitor was going (auth/oauth-return.ts). That return is a document of its own: its first page takes what
// was kept, and any other first page drops it, so a later visit to the root is not sent anywhere. A preload is no visit.
let landing = true;
const root = createRootRoute({
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

// A page a mailed link opens that did not load. The link's token was only in memory (auth/mail-link.ts), which a reload
// clears (the router reloads once by itself when a page's code fails to load), so the way back is the email's link.
function MailLinkFailed() {
  useLoginLinkFailed();
  const { t } = useTranslation("login");
  return <PageFailedState body={t("auth.linkFailed")} />;
}

// The options of a route a mailed link opens: its token leaves the address before the page's code loads, and is kept
// for the page until it is left (auth/mail-link.ts).
const mailLinkRoute = (to: MailRoute) => ({
  beforeLoad: ({ location, preload }: { location: ParsedLocation; preload: boolean }) => {
    if (preload || !location.hash) return;
    keepMailLink(to, location.hash);
    throw redirect({ to, search: location.search as never, replace: true });
  },
  onLeave: () => dropMailLink(to),
  errorComponent: MailLinkFailed,
});

// The sign-in pages sit outside the shell. They remember where the visitor was going, to take them back after signing
// in, and a registration link can carry the invite code.
const guestSearch = (search: Record<string, unknown>) => ({
  next: safeNext(search.next),
  invite: typeof search.invite === "string" ? search.invite : undefined,
});

const app = createRoute({ getParentRoute: () => root, id: "app", component: Layout });

// The sign-in pages stand in one frame (auth/login-frame.tsx LoginLayout), made once for all of them: going from one to
// another changes only the question. While its code or a page's is on the way the window shows the page's ground, not
// the console's skeleton: the frame and its question come as one.
const blank = () => null;
const login = createRoute({
  getParentRoute: () => root,
  id: "login",
  pendingComponent: blank,
  component: lazyRouteComponent(() => import("./auth/login-frame"), "LoginLayout"),
});
// a page gone to from another keeps that one's question on show while its code comes, up to a second
const signInPage = { getParentRoute: () => login, pendingComponent: blank, pendingMs: 1000 };

// Every page is its own chunk, loaded when the user is about to open it; the first screen loads the page of the address
// it opens (scripts/budget.ts counts it).
const routeTree = root.addChildren([
  login.addChildren([
    createRoute({
      ...signInPage,
      path: "/sign-in",
      validateSearch: guestSearch,
      component: lazyRouteComponent(() => import("./auth/sign-in")),
    }),
    createRoute({
      ...signInPage,
      path: "/mfa",
      validateSearch: guestSearch,
      component: lazyRouteComponent(() => import("./auth/mfa")),
    }),
    createRoute({
      ...signInPage,
      path: "/register",
      validateSearch: guestSearch,
      component: lazyRouteComponent(() => import("./auth/register")),
    }),
    createRoute({
      ...signInPage,
      path: "/verify-email",
      validateSearch: guestSearch,
      ...mailLinkRoute("/verify-email"),
      component: lazyRouteComponent(() => import("./auth/verify-email")),
    }),
    createRoute({
      ...signInPage,
      path: "/reset-password",
      validateSearch: guestSearch,
      ...mailLinkRoute("/reset-password"),
      component: lazyRouteComponent(() => import("./auth/reset-password")),
    }),
    createRoute({
      ...signInPage,
      path: "/consent",
      validateSearch: guestSearch,
      component: lazyRouteComponent(() => import("./auth/consent")),
    }),
  ]),
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
      path: "/request-presets",
      component: lazyRouteComponent(() => import("./pages/request-presets")),
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
