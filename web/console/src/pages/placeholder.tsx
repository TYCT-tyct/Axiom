import { EmptyState, PageHeader } from "@axiom/ui";
import { useTranslation } from "react-i18next";

// Placeholder stands in for a page that Issue #30 builds.
export function Placeholder({ page }: { page: "overview" | "keys" | "logs" }) {
  const { t } = useTranslation();
  return (
    <>
      <PageHeader title={t(`nav.${page}`)} description={t(`${page}.description`)} />
      <EmptyState>
        <p>{t("comingSoon")}</p>
      </EmptyState>
    </>
  );
}
