import { Button } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { SiteShell } from "./shell";

export function Home() {
  const { t } = useTranslation();
  return (
    <SiteShell mainClassName="justify-center gap-6 py-24">
      <h1 className="max-w-[16em] text-[clamp(36px,7vw,68px)] leading-[1.08] tracking-[-0.035em] text-t1">
        {t("hero.title")}
      </h1>
      <p className="max-w-[34em] text-[17px] leading-relaxed text-t2">{t("hero.body")}</p>
      <div>
        <Button asChild variant="primary" size="lg">
          <a href="/console/">{t("hero.start")}</a>
        </Button>
      </div>
    </SiteShell>
  );
}
