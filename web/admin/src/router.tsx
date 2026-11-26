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
import { supplierStatus } from "./suppliers/statuses";
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
) => createRoute({ getParentRoute: () => app, path, validateSearch, component: lazyRouteComponent(load) });

// A page that has moved: its old address takes the visitor to where it is now, and is never shown.
type Moved = { to: string; search?: Record<string, string> };
const movedPage = (path: string, to: (params: Record<string, string | undefined>) => Moved) =>
  createRoute({
    getParentRoute: () => app,
    path,
    beforeLoad: ({ params }) => {
      throw redirect({ ...to(params), replace: true } as never);
    },
  });

// A filter in the address is text, or nothing.
const text = (value: unknown) => (typeof value === "string" && value.trim() !== "" ? value : undefined);

// A settings page, which the palette reaches with the row to flash named in the address (settings-drafts.tsx).
const settingsPage = (path: string, load: () => Promise<{ default: () => ReactNode }>) =>
  shellPage(path, load, (search) => ({ setting: text(search.setting) }));

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
    shellPage(
      "/users",
      () => import("./pages/users"),
      (search) => ({ q: text(search.q) }),
    ),
    shellPage("/accounts/$accountId", () => loadTranslatedPage(import("./pages/account"))),
    shellPage(
      "/orders",
      () => import("./pages/orders"),
      (search) => ({
        account_id: text(search.account_id),
        user_id: text(search.user_id),
        state: paymentOrderState(search.state),
        merchant_id: text(search.merchant_id),
        cash: cashView(search.cash),
        paid: text(search.paid),
        since: text(search.since),
        until: text(search.until),
      }),
    ),
    shellPage(
      "/redeem-codes",
      () => import("./pages/redeem-codes"),
      (search) => ({ state: redeemState(search.state), note: text(search.note) }),
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
    shellPage(
      "/payment-discrepancies",
      () => loadTranslatedPage(import("./pages/payment-discrepancies")),
      (search) => ({ state: search.state === "open" || search.state === "resolved" ? search.state : undefined }),
    ),
    shellPage("/payment-discrepancies/$discrepancyId", () => loadTranslatedPage(import("./pages/payment-discrepancy"))),
    shellPage(
      "/subscription-plans",
      () => loadTranslatedPage(import("./pages/subscription-plans")),
      (search) => ({
        include_inactive: search.include_inactive === true || search.include_inactive === "true" ? true : undefined,
      }),
    ),
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
