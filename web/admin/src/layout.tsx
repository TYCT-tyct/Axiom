import { Shell } from "@axiom/app";
import { Outlet } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { nav } from "./nav";

export function Layout() {
  const { t } = useTranslation();
  return (
    <Shell brand={{ name: t("brandName") }} nav={nav.map((item) => ({ ...item, label: t(item.label) }))}>
      <Outlet />
    </Shell>
  );
}
