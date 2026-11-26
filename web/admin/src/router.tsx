import { safeNext } from "@axiom/app";
import { focusPage, NotFoundState, routerDefaults } from "@axiom/ui";
import {
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
  Outlet,
  redirect,
} from "@tanstack/react-router";
import type { ReactNode } from "react";
import { approvalState } from "./approvals";
import { Layout } from "./layout";
import { loadTranslatedPage } from "./locales/load-page";
import { modelStatus } from "./models/catalog";
import { paymentOrderState } from "./orders/states";
import { callStatus, cashView, overviewRange } from "./overview";
import { callPlugin } from "./plugins";
import { redeemState } from "./redeem";
import { tabOf } from "./tab-of";

const root = createRootRoute({ component: Outlet });

// The sign-in pages sit outside the shell. They remember where the visitor was going, to take them back after signing
// in.
const guestSearch = (search: Record<string, unknown>) => ({ next: safeNext(search.next) });

const app = createRoute({ getParentRoute: () => root, id: "app", component: Layout });

const guestPage = (path: "/sign-in" | "/mfa" | "/enroll", load: () => Promise<{ default: () => ReactNode }>) =>
  createRoute({
    getParentRoute: () => root,
    path,
    validateSearch: guestSearch,
    component: lazyRouteComponent(load),
  });

// A page of the shell, its own chunk, loaded when the user is about to open it.
const shellPage = <S extends Record<string, unknown> = Record<never, never>>(
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
const movedPage = <S extends Record<string, unknown> = Record<never, never>>(
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
const text = (value: unknown) => (typeof value === "string" && value.trim() !== "" ? value : undefined);

// A settings page, which the palette reaches with the row to flash named in the address (settings-drafts.tsx).
const settingsPage = (path: string, load: () => Promise<{ default: () => ReactNode }>) =>
  shellPage(path, load, (search) => ({ setting: text(search.setting) }));

const users = () => loadTranslatedPage(import("./pages/users"));
const usersSearch = (search: Record<string, unknown>) => ({ q: text(search.q), selected: text(search.selected) });
const subscriptions = () => loadTranslatedPage(import("./pages/subscriptions"));
const orders = () => loadTranslatedPage(import("./pages/orders"));
const ordersWindow = (search: Record<string, unknown>) => ({
  since: text(search.since),
  until: text(search.until),
  selected: text(search.selected),
});
const withInactive = (search: Record<string, unknown>) => ({
  include_inactive: search.include_inactive === true || search.include_inactive === "true" ? true : undefined,
});
const discrepancyState = (value: unknown) => (value === "open" || value === "resolved" ? value : undefined);

const routeTree = root.addChildren([
  guestPage("/sign-in", () => import("./auth/sign-in")),
  guestPage("/mfa", () => import("./auth/mfa")),
  guestPage("/enroll", () => import("./auth/enroll")),
  app.addChildren([
    shellPage(
      "/",
      () => loadTranslatedPage(import("./pages/home")),
      (search) => ({ range: overviewRange(search.range) }),
    ),
    shellPage(
      "/monitor",
      () => loadTranslatedPage(import("./pages/monitor")),
      (search) => ({ window: search.window === "24h" ? ("24h" as const) : ("1h" as const) }),
    ),
    shellPage(
      "/approvals",
      () => import("./pages/approvals"),
      (search) => ({
        state: approvalState(search.state),
        view: search.view === "done" ? ("done" as const) : undefined,
      }),
    ),
    shellPage("/approvals/$approvalId", () => import("./pages/approval")),
    shellPage("/users", users, usersSearch),
    shellPage("/users/accounts", users, usersSearch),
    shellPage("/users/roles", users, usersSearch),
    movedPage("/accounts/$accountId", ({ accountId }) => ({ to: "/users/accounts", search: { selected: accountId } })),
    shellPage("/subscriptions", subscriptions, usersSearch),
    shellPage("/subscriptions/plans", subscriptions, withInactive),
    movedPage("/subscription-plans", (_, search) => ({ to: "/subscriptions/plans", search }), withInactive),
    // The dashboard's drill-downs from before the tabs, /orders?cash=1 and /orders?paid=batches, with their window.
    shellPage(
      "/orders",
      orders,
      (search) => ({
        account_id: text(search.account_id),
        user_id: text(search.user_id),
        state: paymentOrderState(search.state),
        merchant_id: text(search.merchant_id),
        cash: cashView(search.cash),
        paid: text(search.paid),
        since: text(search.since),
        until: text(search.until),
        selected: text(search.selected),
      }),
      ({ cash, paid, since, until }) => {
        if (cash || paid === "batches") {
          throw redirect({ to: cash ? "/orders/cash" : "/orders/batches", search: { since, until } });
        }
      },
    ),
    shellPage("/orders/cash", orders, ordersWindow),
    shellPage("/orders/batches", orders, ordersWindow),
    shellPage("/orders/refunds", orders, ordersWindow),
    shellPage("/orders/discrepancies", orders, (search) => ({
      state: discrepancyState(search.state),
      selected: text(search.selected),
    })),
    movedPage(
      "/payment-discrepancies",
      (_, search) => ({ to: "/orders/discrepancies", search }),
      (search) => ({ state: discrepancyState(search.state) }),
    ),
    movedPage("/payment-discrepancies/$discrepancyId", ({ discrepancyId }) => ({
      to: "/orders/discrepancies",
      search: { selected: discrepancyId },
    })),
    shellPage(
      "/redeem-codes",
      () => loadTranslatedPage(import("./pages/redeem-codes")),
      (search) => ({ state: redeemState(search.state), note: text(search.note), selected: text(search.selected) }),
    ),
    shellPage(
      "/calls",
      () => loadTranslatedPage(import("./pages/calls")),
      (search) => ({
        account_id: text(search.account_id),
        request_id: text(search.request_id),
        site_id: text(search.site_id),
        model: text(search.model),
        status: callStatus(search.status),
        plugin: callPlugin(search.plugin),
        since: text(search.since),
        until: text(search.until),
      }),
    ),
    shellPage(
      "/audit",
      () => loadTranslatedPage(import("./pages/audit")),
      (search) => ({
        since: text(search.since),
        until: text(search.until),
        action: text(search.action),
        module: text(search.module),
        actor_kind: text(search.actor_kind),
        actor_id: text(search.actor_id),
        target_kind: text(search.target_kind),
        target_id: text(search.target_id),
        account_id: text(search.account_id),
        site_id: text(search.site_id),
        request_id: text(search.request_id),
      }),
    ),
    shellPage(
      "/suppliers",
      () => loadTranslatedPage(import("./pages/suppliers")),
      (search) => ({
        tab: tabOf(search.tab, ["models", "inbox", "keys"] as const),
        credential: text(search.credential),
      }),
    ),
    shellPage(
      "/suppliers/$supplierId",
      () => loadTranslatedPage(import("./pages/supplier")),
      (search) => ({
        tab: tabOf(search.tab, ["models", "keys", "sync", "settings"] as const),
        endpoint: text(search.endpoint),
        credential: text(search.credential),
      }),
    ),
    // The queue of unmapped names became 上游's 待处理.
    movedPage("/mappings", () => ({ to: "/suppliers", search: { tab: "inbox" } })),
    shellPage(
      "/models",
      () => import("./pages/models"),
      (search) => ({
        tab: tabOf(search.tab, ["authors"] as const),
        status: modelStatus(search.status),
        author: text(search.author),
        q: text(search.q),
        model: text(search.model),
      }),
    ),
    shellPage(
      "/routing",
      () => loadTranslatedPage(import("./pages/routing")),
      (search) => ({ tab: tabOf(search.tab, ["defaults", "sources"] as const), preset: text(search.preset) }),
    ),
    shellPage(
      "/pricing",
      () => loadTranslatedPage(import("./pages/pricing")),
      (search) => ({ tab: tabOf(search.tab, ["rules", "rates", "policy"] as const), price: text(search.price) }),
    ),
    // The pages the design folded into tabs and drawers keep their addresses: a link, a bookmark or the bell's old
    // target lands on the tab or the open row that has them now.
    movedPage("/models/$modelId", ({ modelId = "" }) => ({ to: "/models", search: { model: modelId } })),
    movedPage("/routing-presets", () => ({ to: "/routing" })),
    movedPage("/source-classes", () => ({ to: "/routing", search: { tab: "sources" } })),
    movedPage("/exchange-rates", () => ({ to: "/pricing", search: { tab: "rates" } })),
    shellPage("/plugins", () => loadTranslatedPage(import("./pages/plugins"))),
    shellPage("/plugins/web", () => loadTranslatedPage(import("./pages/plugins"))),
    shellPage("/plugins/file", () => loadTranslatedPage(import("./pages/plugins"))),
    shellPage("/me", () => loadTranslatedPage(import("./pages/me"))),
    shellPage("/alert-events", () => loadTranslatedPage(import("./pages/alert-events"))),
    settingsPage("/alert-configuration", () => loadTranslatedPage(import("./pages/alert-configuration"))),
    shellPage("/announcements", () => loadTranslatedPage(import("./pages/announcements"))),
    shellPage("/legal-documents", () => loadTranslatedPage(import("./pages/legal-documents"))),
    shellPage("/rate-limits", () => loadTranslatedPage(import("./pages/rate-limits"))),
    settingsPage("/settings/site", () => loadTranslatedPage(import("./pages/settings-site"))),
    settingsPage("/settings/sign-in", () => loadTranslatedPage(import("./pages/settings-sign-in"))),
    settingsPage("/settings/registration", () => loadTranslatedPage(import("./pages/settings-registration"))),
    settingsPage("/settings/sessions", () => loadTranslatedPage(import("./pages/settings-sessions"))),
    settingsPage("/settings/limits", () => loadTranslatedPage(import("./pages/settings-limits"))),
    settingsPage("/settings/payments", () => loadTranslatedPage(import("./pages/settings-payments"))),
    settingsPage("/settings/mail", () => loadTranslatedPage(import("./pages/settings-mail"))),
    settingsPage("/settings/gateway", () => loadTranslatedPage(import("./pages/settings-gateway"))),
    settingsPage("/settings/agent", () => loadTranslatedPage(import("./pages/settings-agent"))),
    // Where the pages of the earlier settings went (adAlias): a link or a bookmark still lands on the page.
    movedPage("/settings", () => ({ to: "/settings/site" })),
    movedPage("/retention", () => ({ to: "/settings/gateway" })),
    movedPage("/operations-credentials", () => ({ to: "/settings/agent" })),
    movedPage("/operations-limits", () => ({ to: "/settings/agent" })),
    createRoute({ getParentRoute: () => app, path: "$", component: NotFoundState }),
  ]),
]);

export const router = createRouter({ ...routerDefaults, routeTree, basepath: import.meta.env.BASE_URL });

router.subscribe("onResolved", focusPage);

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
