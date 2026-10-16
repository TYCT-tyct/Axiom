import { AppShell, Button, Icon, type IconName, Logo, PreferenceSwitches, SideNavItem } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, Outlet } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { SignOutError } from "./parts";
import { siteQuery } from "./queries";
import { can, Gate, useSession, useSignOut } from "./session";
import { NewVersionNotice } from "./version";

const nav: {
  to: "/" | "/keys" | "/calls" | "/balance" | "/security";
  label: string;
  icon: IconName;
  permission?: string;
}[] = [
  { to: "/", label: "nav.overview", icon: "grid" },
  { to: "/keys", label: "nav.keys", icon: "key", permission: "api_keys.view" },
  { to: "/calls", label: "nav.calls", icon: "list", permission: "calls.view" },
  { to: "/balance", label: "nav.balance", icon: "card", permission: "balance.view" },
  { to: "/security", label: "nav.security", icon: "shield" },
];

export function Layout() {
  return (
    <Gate stage="member">
      <Shell />
    </Gate>
  );
}

function Shell() {
  const { t } = useTranslation();
  const session = useSession();
  const site = useQuery(siteQuery);
  return (
    <AppShell
      banner={<NewVersionNotice />}
      sidebar={
        <>
          <Logo className="mb-4 px-2 py-1" name={site.data?.name} logoUrl={site.data?.logo_url} />
          {nav
            .filter((item) => item.permission === undefined || can(session, item.permission))
            .map((item) => (
              <SideNavItem key={item.to}>
                <Link to={item.to} activeOptions={{ exact: item.to === "/" }}>
                  <Icon name={item.icon} />
                  {t(item.label as "nav.overview")}
                </Link>
              </SideNavItem>
            ))}
          <div className="mt-auto space-y-3 pt-4">
            <Account />
            <PreferenceSwitches className="flex flex-wrap gap-2" />
          </div>
        </>
      }
    >
      <Outlet />
    </AppShell>
  );
}

function Account() {
  const { t } = useTranslation();
  const session = useSession();
  const signOut = useSignOut();
  return (
    <div className="space-y-1 border-t border-b1 pt-3">
      <p className="truncate px-2 text-label text-t3" title={session.user.email}>
        {session.user.email}
      </p>
      <Button
        variant="ghost"
        className="w-full justify-start"
        onClick={() => signOut.mutate()}
        disabled={signOut.isPending}
      >
        <Icon name="logout" />
        {t("nav.signOut")}
      </Button>
      <div className="px-2">
        <SignOutError error={signOut.error} />
      </div>
    </div>
  );
}
