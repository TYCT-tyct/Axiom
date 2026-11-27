import { safeNext } from "@axiom/app";
import { dropMailLink, keepMailLink, type MailRoute, useLoginLinkFailed } from "@axiom/login";
import { PageFailedState } from "@axiom/ui";
import { createRoute, lazyRouteComponent, type ParsedLocation, redirect } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { root } from "./shell";

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

// The sign-in pages stand in one frame (auth/login-frame.tsx LoginLayout), made once for all of them: going from one to
// another changes only the question. While its code or a page's is on the way the window shows the page's ground, not
// the console's skeleton: the frame and its question come as one.
const blank = () => null;
const login = createRoute({
  getParentRoute: () => root,
  id: "login",
  pendingComponent: blank,
  component: lazyRouteComponent(() => import("../auth/login-frame"), "LoginLayout"),
});
// a page gone to from another keeps that one's question on show while its code comes, up to a second
const signInPage = { getParentRoute: () => login, pendingComponent: blank, pendingMs: 1000 };

export const authRoutes = login.addChildren([
  createRoute({
    ...signInPage,
    path: "/sign-in",
    validateSearch: guestSearch,
    component: lazyRouteComponent(() => import("../auth/sign-in")),
  }),
  createRoute({
    ...signInPage,
    path: "/mfa",
    validateSearch: guestSearch,
    component: lazyRouteComponent(() => import("../auth/mfa")),
  }),
  createRoute({
    ...signInPage,
    path: "/register",
    validateSearch: guestSearch,
    component: lazyRouteComponent(() => import("../auth/register")),
  }),
  createRoute({
    ...signInPage,
    path: "/verify-email",
    validateSearch: guestSearch,
    ...mailLinkRoute("/verify-email"),
    component: lazyRouteComponent(() => import("../auth/verify-email")),
  }),
  createRoute({
    ...signInPage,
    path: "/reset-password",
    validateSearch: guestSearch,
    ...mailLinkRoute("/reset-password"),
    component: lazyRouteComponent(() => import("../auth/reset-password")),
  }),
  createRoute({
    ...signInPage,
    path: "/consent",
    validateSearch: guestSearch,
    component: lazyRouteComponent(() => import("../auth/consent")),
  }),
]);
