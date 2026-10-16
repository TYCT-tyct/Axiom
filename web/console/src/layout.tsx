import { Shell } from "@axiom/app";
import type { IconName } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Outlet } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { siteQuery } from "./queries";

const nav = [
  { to: "/", label: "nav.overview", icon: "grid" },
  { to: "/keys", label: "nav.keys", icon: "key", permission: "api_keys.view" },
  { to: "/calls", label: "nav.calls", icon: "list", permission: "calls.view" },
  { to: "/balance", label: "nav.balance", icon: "card", permission: "balance.view" },
  { to: "/security", label: "nav.security", icon: "shield" },
] as const satisfies readonly { to: string; label: string; icon: IconName; permission?: string }[];

export function Layout() {
  const { t } = useTranslation();
  const site = useQuery(siteQuery);
  return (
    <Shell brand={site.data} nav={nav.map((item) => ({ ...item, label: t(item.label) }))}>
      <Outlet />
    </Shell>
  );
}
