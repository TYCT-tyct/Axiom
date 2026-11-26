import { safeNext } from "@axiom/app";
import { createRoute, lazyRouteComponent } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { root } from "./shell";

// The sign-in pages sit outside the shell. They remember where the visitor was going, to take them back after signing
// in.
const guestSearch = (search: Record<string, unknown>) => ({ next: safeNext(search.next) });

const guestPage = (path: "/sign-in" | "/mfa" | "/enroll", load: () => Promise<{ default: () => ReactNode }>) =>
  createRoute({
    getParentRoute: () => root,
    path,
    validateSearch: guestSearch,
    component: lazyRouteComponent(load),
  });

export const authRoutes = [
  guestPage("/sign-in", () => import("../auth/sign-in")),
  guestPage("/mfa", () => import("../auth/mfa")),
  guestPage("/enroll", () => import("../auth/enroll")),
] as const;
