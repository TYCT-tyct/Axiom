import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { Button, Logo, PreferenceSwitches } from "@axiom/ui";
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

// The console lives on the same listener under /console/ (console/vite.config.ts).
const consoleHref = "/console/";

type Brand = Pick<Schemas["Site"], "logo_url" | "api_base_url"> & { name?: string };

// The site's name, logo and API address, from the same public call the console's sign-in page uses. A host the API does not know, or
// a failed request, leaves the default mark and name: the page is still the product's.
export function useSiteBrand(): Brand | undefined {
  const [brand, setBrand] = useState<Brand>();
  useEffect(() => {
    const call = new AbortController();
    unwrap(consoleApi.GET("/api/console/v1/site", { signal: call.signal }))
      .then((site) => {
        if (!call.signal.aborted) {
          setBrand({ name: site.name || undefined, logo_url: site.logo_url, api_base_url: site.api_base_url });
        }
      })
      .catch(() => {});
    return () => call.abort();
  }, []);
  return brand;
}

// SiteShell is the website's frame: the site's mark, the public pages and the way into the console. The pages are
// plain links, not a client router, because the website has only a few pages and each opens as its own document.
export function SiteShell({ children, mainClassName = "" }: { children: ReactNode; mainClassName?: string }) {
  const { t } = useTranslation();
  const brand = useSiteBrand();
  return (
    <div className="flex min-h-dvh flex-col">
      <header className="mx-auto flex h-16 w-full max-w-[1248px] items-center gap-4 px-4 sm:px-6">
        <a href="/" className="min-w-0 shrink" aria-label={t("home")}>
          <Logo name={brand?.name} logoUrl={brand?.logo_url} />
        </a>
        <nav aria-label={t("navigation")} className="flex shrink-0 items-center gap-1">
          <a
            href="/models"
            className="rounded-xs px-2 py-1 text-ui text-t2 underline-offset-4 hover:text-t1 hover:underline"
          >
            {t("models")}
          </a>
        </nav>
        <PreferenceSwitches className="ml-auto hidden gap-2 sm:flex" />
        <Button asChild size="sm" className="ml-auto shrink-0 sm:ml-0">
          <a href={consoleHref}>{t("signIn")}</a>
        </Button>
      </header>
      <main className={`mx-auto flex w-full max-w-[1248px] flex-1 flex-col px-4 sm:px-6 ${mainClassName}`}>
        {children}
      </main>
      <footer className="mx-auto flex w-full max-w-[1248px] flex-wrap items-center gap-4 border-t border-b1 px-4 py-6 text-ui text-t4 sm:px-6">
        {t("footer")}
        <PreferenceSwitches className="ml-auto flex gap-2 sm:hidden" />
      </footer>
    </div>
  );
}
