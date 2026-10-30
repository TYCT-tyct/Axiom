import { safeNext } from "@axiom/app";
import { focusPage, NotFoundState, routerDefaults } from "@axiom/ui";
import { createRootRoute, createRoute, createRouter, lazyRouteComponent, Outlet } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { Layout } from "./layout";
import { modelStatus } from "./models/catalog";
import { paymentOrderState } from "./orders/states";
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
) => createRoute({ getParentRoute: () => app, path, validateSearch, component: lazyRouteComponent(load) });

// A filter in the address is text, or nothing.
const text = (value: unknown) => (typeof value === "string" && value.trim() !== "" ? value : undefined);

const routeTree = root.addChildren([
  guestPage("/sign-in", () => import("./auth/sign-in")),
  guestPage("/mfa", () => import("./auth/mfa")),
  guestPage("/enroll", () => import("./auth/enroll")),
  app.addChildren([
    shellPage("/", () => import("./pages/home")),
    shellPage(
      "/users",
      () => import("./pages/users"),
      (search) => ({ q: text(search.q) }),
    ),
    shellPage("/accounts/$accountId", () => import("./pages/account")),
    shellPage(
      "/orders",
      () => import("./pages/orders"),
      (search) => ({
        account_id: text(search.account_id),
        user_id: text(search.user_id),
        state: paymentOrderState(search.state),
        merchant_id: text(search.merchant_id),
        since: text(search.since),
        until: text(search.until),
      }),
    ),
    shellPage(
      "/calls",
      () => import("./pages/calls"),
      (search) => ({
        account_id: text(search.account_id),
        request_id: text(search.request_id),
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
      () => import("./pages/suppliers"),
      (search) => ({ status: supplierStatus(search.status) }),
    ),
    shellPage("/suppliers/$supplierId", () => import("./pages/supplier")),
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
    shellPage("/pricing", () => import("./pages/pricing")),
    shellPage("/settings", () => import("./pages/settings")),
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
