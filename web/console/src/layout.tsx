import { Shell } from "@axiom/app";
import type { IconName } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Outlet } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { siteQuery } from "./queries";

const nav = [
  { to: "/", label: "nav.overview", icon: "grid" },
  { to: "/keys", label: "nav.keys", icon: "key", permission: "api_keys.view" },
  { to: "/provider-keys", label: "nav.providerKeys", icon: "lock", permission: "api_keys.view" },
  { to: "/calls", label: "nav.calls", icon: "list", permission: "calls.view" },
  { to: "/models", label: "nav.models", icon: "cube" },
  { to: "/balance", label: "nav.balance", icon: "card", permission: "balance.view" },
  { to: "/redeem", label: "nav.redeem", icon: "card", permission: "balance.view" },
  { to: "/orders", label: "nav.orders", icon: "list", permission: "balance.view" },
  { to: "/statement", label: "nav.statement", icon: "chart", permission: "balance.view" },
  { to: "/security", label: "nav.security", icon: "shield" },
  { to: "/notifications", label: "nav.notifications", icon: "sliders" },
  { to: "/settings", label: "nav.settings", icon: "sliders" },
] as const satisfies readonly { to: string; label: string; icon: IconName; permission?: string }[];

export function Layout() {
  const { t } = useTranslation();
  const site = useQuery(siteQuery);
  const items = nav.map((item) => ({ ...item, label: t(item.label) }));
  return (
    <Shell brand={site.data} nav={items} forbiddenHint={t("forbiddenHint")}>
      <Outlet />
    </Shell>
  );
}
