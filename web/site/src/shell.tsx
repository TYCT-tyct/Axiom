import { consoleApi, isTransient, type Registration, registrationOf, type Schemas, unwrap } from "@axiom/api";
import { Button, Logo, PreferenceSwitches, useBrandStyle } from "@axiom/ui";
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

// The console lives on the same listener under /console/ (console/vite.config.ts).
const consoleHref = "/console/";

export type Brand = Pick<Schemas["Site"], "logo_url" | "api_base_url" | "brand_color"> & {
  name?: string;
  registration: Registration;
};

// What the page knows of its site: still asking, the call failed (asked again when the failure may pass), or the answer.
export type SiteState = { status: "pending" } | { status: "failed" } | { status: "ready"; brand: Brand };

// A failed call is asked again after a growing wait, up to this, and at once when the browser is back online.
const retryCap = 30_000;

const brandOf = (site: Schemas["Site"]): Brand => ({
  name: site.name || undefined,
  logo_url: site.logo_url,
  api_base_url: site.api_base_url,
  registration: registrationOf(site),
  brand_color: site.brand_color,
});

// useSiteState asks for the site's name, logo, API address and how it takes new users, from the same public call the
// console's sign-in page uses, once for the page: what promises depend on the answer waits for it, a call that failed
// on the network or the server is asked again, and a page the browser brings back from its cache asks afresh, since the settings may have changed while
// it was away. The site's brand colour is put on the document's root (useBrandStyle, as the console's shells do), so
// every part of the page takes it. A host the API does not know leaves the default mark, name and colours.
export function useSiteState(): SiteState {
  const [state, setState] = useState<SiteState>({ status: "pending" });
  useBrandStyle(state.status === "ready" ? state.brand.brand_color : undefined);
  useEffect(() => {
    let call = new AbortController();
    let timer = 0;
    let failures = 0;
    const ask = () => {
      call.abort();
      window.clearTimeout(timer);
      const mine = new AbortController();
      call = mine;
      unwrap(consoleApi.GET("/api/console/v1/site", { signal: mine.signal })).then(
        (site) => {
          if (mine.signal.aborted) return;
          failures = 0;
          setState({ status: "ready", brand: brandOf(site) });
        },
        (error: unknown) => {
          if (mine.signal.aborted) return;
          setState({ status: "failed" });
          // a host the API does not know answers the same every time; a failure of the network or the server may pass
          if (isTransient(error)) timer = window.setTimeout(ask, Math.min(retryCap, 1000 * 2 ** failures++));
        },
      );
    };
    const onShow = (e: PageTransitionEvent) => {
      if (!e.persisted) return;
      setState({ status: "pending" });
      ask();
    };
    const onOnline = () => {
      if (failures > 0) ask();
    };
    ask();
    window.addEventListener("pageshow", onShow);
    window.addEventListener("online", onOnline);
    return () => {
      call.abort();
      window.clearTimeout(timer);
      window.removeEventListener("pageshow", onShow);
      window.removeEventListener("online", onOnline);
    };
  }, []);
  return state;
}

// SiteShell is the website's frame: the site's mark, the public pages and the way into the console. The pages are
// plain links, not a client router, because the website has only a few pages and each opens as its own document.
export function SiteShell({ children, mainClassName = "" }: { children: ReactNode; mainClassName?: string }) {
  const { t } = useTranslation();
  const site = useSiteState();
  const brand = site.status === "ready" ? site.brand : undefined;
  const name = brand?.name ?? t("brand", { ns: "common" });
  return (
    <div className="flex min-h-dvh flex-col">
      <header className="mx-auto flex h-16 w-full max-w-[1248px] items-center gap-4 px-4 sm:px-6">
        <a href="/" className="min-w-0 shrink" aria-label={t("home", { site: name })}>
          <Logo name={name} logoUrl={brand?.logo_url} />
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
        <div className="ml-auto shrink-0 sm:ml-0">
          <Button asChild size="sm">
            <a href={consoleHref}>{t("signIn")}</a>
          </Button>
        </div>
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
