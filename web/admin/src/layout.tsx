import { Shell } from "@axiom/app";
import { Outlet } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useAdminBrand } from "./brand";
import { nav } from "./nav";

export function Layout() {
  const { t } = useTranslation();
  const items = nav.map((item) => ({ ...item, label: t(item.label) }));
  const brand = useAdminBrand();
  return (
    <Shell brand={brand} nav={items} forbiddenHint={t("forbiddenHint")}>
      <Outlet />
    </Shell>
  );
}
