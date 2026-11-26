import { siteQuery } from "@axiom/login";
import { createRoute, lazyRouteComponent } from "@tanstack/react-router";
import { balanceQuery, ordersQuery, redemptionsQuery, statementQuery } from "../queries";
import { asking } from "../warm";
import { app } from "./shell";

// The balance, redeeming, the orders and their receipts, and the statement.
export const billingRoutes = [
  createRoute({
    getParentRoute: () => app,
    path: "/balance",
    // the plans (#plans) and a top-up under way (its order, kept in the tab) ask for more of their own
    loader: asking(balanceQuery),
    component: lazyRouteComponent(() => import("../pages/balance")),
  }),
  createRoute({
    getParentRoute: () => app,
    path: "/redeem",
    loader: asking(redemptionsQuery),
    component: lazyRouteComponent(() => import("../pages/redeem")),
  }),
  createRoute({
    getParentRoute: () => app,
    path: "/orders",
    loader: asking(ordersQuery, balanceQuery, siteQuery),
    component: lazyRouteComponent(() => import("../pages/orders")),
  }),
  createRoute({
    getParentRoute: () => app,
    path: "/orders/$orderId/receipt",
    component: lazyRouteComponent(() => import("../pages/receipt")),
  }),
  createRoute({
    getParentRoute: () => app,
    path: "/statement",
    loader: asking(statementQuery),
    component: lazyRouteComponent(() => import("../pages/statement")),
  }),
] as const;
