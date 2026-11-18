import ModelsPage from "@axiom/boards/Models/regions/ModelsPage";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { SiteShell } from "../shell";
import styles from "./models-frame.module.css";
import { useWide } from "./wide";

export function ModelsFrame({ filters, children }: { filters: ReactNode; children: ReactNode }) {
  const { t } = useTranslation("common");
  const wide = useWide();
  return (
    <SiteShell mainClassName={styles.main}>
      <ModelsPage
        t={String}
        values={{ layout: wide ? "d" : "m" }}
        title={t("publicModels.title")}
        lede={t("publicModels.description")}
        filters={filters}
      />
      {children}
    </SiteShell>
  );
}
