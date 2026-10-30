import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { Button, Logo, PreferenceSwitches } from "@axiom/ui";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

// The console lives on the same listener under /console/ (console/vite.config.ts).
const consoleHref = "/console/";

type Brand = Pick<Schemas["Site"], "logo_url"> & { name?: string };

// The site's name and logo, from the same public call the console's sign-in page uses. A host the API does not know, or
// a failed request, leaves the default mark and name: the page is still the product's.
function useSiteBrand(): Brand | undefined {
  const [brand, setBrand] = useState<Brand>();
  useEffect(() => {
    const call = new AbortController();
    unwrap(consoleApi.GET("/api/console/v1/site", { signal: call.signal }))
      .then((site) => {
        if (!call.signal.aborted) {
          setBrand({ name: site.name || undefined, logo_url: site.logo_url });
        }
      })
      .catch(() => {});
    return () => call.abort();
  }, []);
  return brand;
}

export function Home() {
  const { t } = useTranslation();
  const brand = useSiteBrand();
  return (
    <div className="flex min-h-dvh flex-col">
      <header className="mx-auto flex h-16 w-full max-w-[1248px] items-center gap-4 px-4 sm:px-6">
        <Logo name={brand?.name} logoUrl={brand?.logo_url} />
        <PreferenceSwitches className="ml-auto hidden gap-2 sm:flex" />
        <Button asChild size="sm" className="ml-auto sm:ml-0">
          <a href={consoleHref}>{t("signIn")}</a>
        </Button>
      </header>
      <main className="mx-auto flex w-full max-w-[1248px] flex-1 flex-col justify-center gap-6 px-4 py-24 sm:px-6">
        <h1 className="max-w-[16em] text-[clamp(36px,7vw,68px)] leading-[1.08] tracking-[-0.035em] text-t1">
          {t("hero.title")}
        </h1>
        <p className="max-w-[34em] text-[17px] leading-relaxed text-t2">{t("hero.body")}</p>
        <div>
          <Button asChild variant="primary" size="lg">
            <a href={consoleHref}>{t("hero.start")}</a>
          </Button>
        </div>
      </main>
      <footer className="mx-auto flex w-full max-w-[1248px] flex-wrap items-center gap-4 border-t border-b1 px-4 py-6 text-ui text-t4 sm:px-6">
        {t("footer")}
        <PreferenceSwitches className="ml-auto flex gap-2 sm:hidden" />
      </footer>
    </div>
  );
}
