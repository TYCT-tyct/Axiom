import { useEffect } from "react";
import { useTranslation } from "react-i18next";

// usePageTitle names the browser tab after the page shown, so tabs can be told apart and a screen reader announces the
// page (WCAG 2.4.2). Without a page it leaves the title alone, for a component that is a whole page only sometimes.
export function usePageTitle(page: string | undefined) {
  const { t } = useTranslation("common");
  const brand = t("brand");
  useEffect(() => {
    if (page === undefined) {
      return;
    }
    document.title = `${page} · ${brand}`;
  }, [page, brand]);
}
