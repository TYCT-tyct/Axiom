import {
  AppShell,
  focusPage,
  Icon,
  type IconName,
  Logo,
  PreferenceSwitches,
  routerDefaults,
  SideNavItem,
} from "@axiom/ui";
import { createRootRoute, createRoute, createRouter, Link, lazyRouteComponent, Outlet } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { NewVersionNotice } from "./version";

const nav: { to: "/" | "/keys" | "/logs"; label: "nav.overview" | "nav.keys" | "nav.logs"; icon: IconName }[] = [
  { to: "/", label: "nav.overview", icon: "grid" },
  { to: "/keys", label: "nav.keys", icon: "key" },
  { to: "/logs", label: "nav.logs", icon: "list" },
];

function Layout() {
  const { t } = useTranslation();
  return (
    <AppShell
      banner={<NewVersionNotice />}
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

const root = createRootRoute({ component: Layout });

// Every page is its own chunk, loaded when the user is about to open it; the first screen loads the page of the address
// it opens (scripts/budget.ts counts it).
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

export const router = createRouter({ ...routerDefaults, routeTree, basepath: import.meta.env.BASE_URL });

router.subscribe("onResolved", focusPage);

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
