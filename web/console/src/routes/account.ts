import { siteQuery } from "@axiom/login";
import { createRoute, lazyRouteComponent } from "@tanstack/react-router";
import { notificationPreferencesQuery } from "../queries";
import { asking, warming } from "../warm";
import { app } from "./shell";

// The account's own pages: security, notifications and settings.
export const accountRoutes = [
  createRoute({
    getParentRoute: () => app,
    path: "/security",
    loader: warming(() => import("../pages/security").then((page) => page.warm)),
    component: lazyRouteComponent(() => import("../pages/security")),
  }),
  createRoute({
    getParentRoute: () => app,
    path: "/notifications",
    loader: asking(notificationPreferencesQuery, siteQuery),
    component: lazyRouteComponent(() => import("../pages/notifications")),
  }),
  createRoute({
    getParentRoute: () => app,
    path: "/settings",
    loader: warming(() => import("../pages/settings").then((page) => page.warm)),
    component: lazyRouteComponent(() => import("../pages/settings")),
  }),
] as const;
