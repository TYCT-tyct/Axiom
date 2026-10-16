import { AppShell, Icon, type IconName, Logo, PreferenceSwitches, SideNavItem } from "@axiom/ui";
import { Link, type LinkProps } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { SignedInAs } from "./parts";
import { can, Gate, useSession } from "./session";
import { NewVersionNotice } from "./version";

// A page the shell links to, with the label already in the user's language. The pages a session may not use (it does not
// hold the permission) are left out; the server decides again on every request.
export type NavItem = { to: LinkProps["to"]; label: string; icon: IconName; permission?: string };

function NavLinks({ nav }: { nav: NavItem[] }) {
  const session = useSession();
  return nav
    .filter((item) => item.permission === undefined || can(session, item.permission))
    .map((item) => (
      <SideNavItem key={item.to}>
        <Link to={item.to} activeOptions={{ exact: item.to === "/" }}>
          <Icon name={item.icon} />
          {item.label}
        </Link>
      </SideNavItem>
    ));
}

// Shell is the frame of every page a member sees: the app's name and logo above the links the app offers, who is signed
// in and the appearance switches below, the notice that a new version is out across the top, and the page beside.
export function Shell({
  brand,
  nav,
  children,
}: {
  brand: { name?: string; logo_url?: string } | undefined;
  nav: NavItem[];
  children: ReactNode;
}) {
  return (
    <Gate stage="member">
      <AppShell
        banner={<NewVersionNotice />}
        sidebar={
          <>
            <Logo className="mb-4 px-2 py-1" name={brand?.name} logoUrl={brand?.logo_url} />
            <NavLinks nav={nav} />
            <div className="mt-auto space-y-3 pt-4">
              <SignedInAs />
              <PreferenceSwitches className="flex flex-wrap gap-2" />
            </div>
          </>
        }
      >
        {children}
      </AppShell>
    </Gate>
  );
}
