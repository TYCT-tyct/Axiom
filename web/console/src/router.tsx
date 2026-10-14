import {
  AppShell,
  focusPage,
  Icon,
  type IconName,
  LoadingState,
  Logo,
  NotFoundState,
  PageFailedState,
  PreferenceSwitches,
  SideNavItem,
} from "@axiom/ui";
import { createRootRoute, createRoute, createRouter, Link, lazyRouteComponent, Outlet } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

const nav: { to: "/" | "/keys" | "/logs"; label: "nav.overview" | "nav.keys" | "nav.logs"; icon: IconName }[] = [
  { to: "/", label: "nav.overview", icon: "grid" },
  { to: "/keys", label: "nav.keys", icon: "key" },
  { to: "/logs", label: "nav.logs", icon: "list" },
];

function Layout() {
  const { t } = useTranslation();
  return (
    <AppShell
      sidebar={
        <>
          <Logo className="mb-4 px-2 py-1" />
          {nav.map((item) => (
            <SideNavItem key={item.to}>
              <Link to={item.to} activeOptions={{ exact: item.to === "/" }}>
                <Icon name={item.icon} />
                {t(item.label)}
              </Link>
            </SideNavItem>
          ))}
          <PreferenceSwitches className="mt-auto flex flex-wrap gap-2 pt-4" />
        </>
      }
    >
      <Outlet />
    </AppShell>
  );
}

function NotFound() {
  const { t } = useTranslation("common");
  return (
    <NotFoundState
      action={
        <Link to="/" className="text-accent underline-offset-4 hover:underline">
          {t("state.home")}
        </Link>
      }
    />
  );
}

const root = createRootRoute({ component: Layout, notFoundComponent: NotFound });

// Every page but the first is its own chunk, loaded when the user is about to open it.
const routeTree = root.addChildren([
  createRoute({
    getParentRoute: () => root,
    path: "/",
    component: lazyRouteComponent(() => import("./pages/overview")),
  }),
  createRoute({
    getParentRoute: () => root,
    path: "/keys",
    component: lazyRouteComponent(() => import("./pages/keys")),
  }),
  createRoute({
    getParentRoute: () => root,
    path: "/logs",
    component: lazyRouteComponent(() => import("./pages/logs")),
  }),
]);

// While the next page loads the current one stays; past a short wait the layout stays and the page area shows a
// skeleton, so navigating never blanks the screen.
export const router = createRouter({
  routeTree,
  basepath: import.meta.env.BASE_URL,
  defaultPreload: "intent",
  defaultPendingMs: 150,
  defaultPendingComponent: LoadingState,
  defaultErrorComponent: PageFailedState,
});

router.subscribe("onResolved", focusPage);

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
