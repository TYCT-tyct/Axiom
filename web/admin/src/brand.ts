import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { adminSiteQuery } from "./queries";

// useAdminBrand is the site's name and logo, as every admin page shows them. Until the public brand arrives, or on a
// node of the release before `getAdminSite`, it is the platform's name and no logo.
export function useAdminBrand() {
  const { t } = useTranslation();
  const site = useQuery(adminSiteQuery);
  return site.data
    ? { name: site.data.name || t("brandName"), logo_url: site.data.logo_url }
    : { name: t("brandName") };
}
