import "@axiom/boards/AxApp/regions.css";
import ModelsPage from "@axiom/boards/Models/regions/ModelsPage";
import { Button, Logo, PreferenceSwitches } from "@axiom/ui";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useSiteState } from "../shell";
import styles from "./models-frame.module.css";
import { useWide } from "./wide";

// The console lives on the same listener under /console/ (console/vite.config.ts).
const consoleHref = "/console/";

export function ModelsFrame({ filters, children }: { filters: ReactNode; children: ReactNode }) {
  const { t } = useTranslation("common");
  const wide = useWide();
  return (
    <SiteShell>
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

// SiteShell is the website's frame: the site's mark, the public pages and the way into the console. The pages are
// plain links, not a client router, because the website has only a few pages and each opens as its own document. The
// preference switches sit in the header from 640px and in the footer below it.
function SiteShell({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const site = useSiteState();
  const brand = site.status === "ready" ? site.brand : undefined;
  const name = brand?.name ?? t("brand", { ns: "common" });
  return (
    <div className={styles.shell}>
      <header className={styles.header}>
        <a href="/" className={styles.home} aria-label={t("home", { site: name })}>
          <Logo name={name} logoUrl={brand?.logo_url} />
        </a>
        <nav aria-label={t("navigation")} className={styles.nav}>
          <a href="/models" className={styles.link}>
            {t("models")}
          </a>
        </nav>
        <div className={styles.switches}>
          <PreferenceSwitches />
        </div>
        <div className={styles.enter}>
          <Button asChild size="sm">
            <a href={consoleHref}>{t("signIn")}</a>
          </Button>
        </div>
      </header>
      <main className={styles.main}>{children}</main>
      <footer className={styles.footer}>
        {t("footer")}
        <div className={styles.footSwitches}>
          <PreferenceSwitches />
        </div>
      </footer>
    </div>
  );
}
