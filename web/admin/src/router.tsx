import {
  AppShell,
  EmptyState,
  focusPage,
  Icon,
  type IconName,
  LoadingState,
  Logo,
  NotFoundState,
  PageFailedState,
  PageHeader,
  PreferenceSwitches,
  SideNavItem,
} from "@axiom/ui";
import { createRootRoute, createRoute, createRouter, Link, Outlet } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

type Page = "overview" | "suppliers" | "models" | "pricing" | "users";

const pages: { page: Page; path: string; icon: IconName }[] = [
  { page: "overview", path: "/", icon: "grid" },
  { page: "suppliers", path: "/suppliers", icon: "cube" },
  { page: "models", path: "/models", icon: "list" },
  { page: "pricing", path: "/pricing", icon: "sliders" },
  { page: "users", path: "/users", icon: "card" },
];

function Layout() {
  const { t } = useTranslation();
  return (
    <AppShell
      sidebar={
        <>
          <Logo className="mb-4 px-2 py-1" />
          {pages.map(({ page, path, icon }) => (
            <SideNavItem key={page}>
              <Link to={path} activeOptions={{ exact: path === "/" }}>
                <Icon name={icon} />
                {t(`nav.${page}`)}
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

// Placeholder stands in for a page that Issue #30 builds.
function Placeholder({ page }: { page: Page }) {
  const { t } = useTranslation();
  return (
    <>
      <PageHeader title={t(`nav.${page}`)} />
      <EmptyState>
        <p>{t("comingSoon")}</p>
      </EmptyState>
    </>
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

const routeTree = root.addChildren(
  pages.map(({ page, path }) =>
    createRoute({ getParentRoute: () => root, path, component: () => <Placeholder page={page} /> }),
  ),
);

export const router = createRouter({
  routeTree,
  defaultPreload: "intent",
  defaultPendingMs: 150,
  defaultPendingComponent: LoadingState,
  defaultErrorComponent: PageFailedState,
});

router.subscribe("onResolved", focusPage);
