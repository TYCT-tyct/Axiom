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
import { redeemState } from "./redeem";
import { supplierStatus } from "./suppliers/statuses";

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

// A filter in the address is text, or nothing.
const text = (value: unknown) => (typeof value === "string" && value.trim() !== "" ? value : undefined);

// An address the design folded into a tab of a page: it leads to the tab, keeping what it was asked for, so a link, a
// bookmark and the dashboard's drill-downs still arrive.
const movedTo = <S extends Record<string, unknown>>(
  path: string,
  validateSearch: (search: Record<string, unknown>) => S,
  to: (params: Record<string, string>, search: S) => { to: string; search: Record<string, unknown> },
) =>
  createRoute({
    getParentRoute: () => app,
    path,
    validateSearch,
    beforeLoad: ({ params, search }) => {
      throw redirect(to(params as Record<string, string>, search as S) as never);
    },
  });

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
      () => import("./pages/home"),
      (search) => ({ range: overviewRange(search.range) }),
    ),
    shellPage(
      "/approvals",
      () => import("./pages/approvals"),
      (search) => ({ state: approvalState(search.state) }),
    ),
    shellPage("/approvals/$approvalId", () => import("./pages/approval")),
    shellPage("/operations-credentials", () => loadTranslatedPage(import("./pages/operations-credentials"))),
    shellPage("/operations-limits", () => loadTranslatedPage(import("./pages/operations-limits"))),
    shellPage("/users", users, usersSearch),
    shellPage("/users/accounts", users, usersSearch),
    shellPage("/users/roles", users, usersSearch),
    movedTo(
      "/accounts/$accountId",
      () => ({}),
      (params) => ({ to: "/users/accounts", search: { selected: params.accountId } }),
    ),
    shellPage("/subscriptions", subscriptions, usersSearch),
    shellPage("/subscriptions/plans", subscriptions, withInactive),
    movedTo("/subscription-plans", withInactive, (_, search) => ({ to: "/subscriptions/plans", search })),
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
    movedTo(
      "/payment-discrepancies",
      (search) => ({ state: discrepancyState(search.state) }),
      (_, search) => ({ to: "/orders/discrepancies", search }),
    ),
    movedTo(
      "/payment-discrepancies/$discrepancyId",
      () => ({}),
      (params) => ({ to: "/orders/discrepancies", search: { selected: params.discrepancyId } }),
    ),
    shellPage(
      "/redeem-codes",
      () => loadTranslatedPage(import("./pages/redeem-codes")),
      (search) => ({ state: redeemState(search.state), note: text(search.note), selected: text(search.selected) }),
    ),
    shellPage(
      "/calls",
      () => import("./pages/calls"),
      (search) => ({
        account_id: text(search.account_id),
        request_id: text(search.request_id),
        site_id: text(search.site_id),
        model: text(search.model),
        status: callStatus(search.status),
        since: text(search.since),
        until: text(search.until),
      }),
    ),
    shellPage("/calls/$callId", () => import("./pages/call")),
    shellPage(
      "/audit",
      () => import("./pages/audit"),
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
    shellPage("/audit/$entryId", () => import("./pages/audit-entry")),
    shellPage(
      "/suppliers",
      () => loadTranslatedPage(import("./pages/suppliers")),
      (search) => ({ status: supplierStatus(search.status) }),
    ),
    shellPage("/suppliers/$supplierId", () => loadTranslatedPage(import("./pages/supplier"))),
    shellPage(
      "/mappings",
      () => import("./pages/mappings"),
      (search) => ({ supplier: text(search.supplier) }),
    ),
    shellPage(
      "/models",
      () => import("./pages/models"),
      (search) => ({
        status: modelStatus(search.status),
        author: text(search.author),
        q: text(search.q),
      }),
    ),
    shellPage("/models/$modelId", () => import("./pages/model")),
    shellPage("/source-classes", () => import("./pages/source-classes")),
    shellPage("/routing-presets", () => loadTranslatedPage(import("./pages/routing-presets"))),
    shellPage("/pricing", () => loadTranslatedPage(import("./pages/pricing"))),
    shellPage("/exchange-rates", () => loadTranslatedPage(import("./pages/exchange-rates"))),
    shellPage("/alert-events", () => loadTranslatedPage(import("./pages/alert-events"))),
    shellPage("/alert-configuration", () => loadTranslatedPage(import("./pages/alert-configuration"))),
    shellPage("/announcements", () => loadTranslatedPage(import("./pages/announcements"))),
    shellPage("/legal-documents", () => loadTranslatedPage(import("./pages/legal-documents"))),
    shellPage("/retention", () => loadTranslatedPage(import("./pages/retention"))),
    shellPage("/rate-limits", () => loadTranslatedPage(import("./pages/rate-limits"))),
    shellPage("/settings", () => loadTranslatedPage(import("./pages/settings"))),
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
