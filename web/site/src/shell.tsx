import { consoleApi, isTransient, type Registration, registrationOf, type Schemas, unwrap } from "@axiom/api";
import { useBrandStyle } from "@axiom/ui";
import { useEffect, useState } from "react";

export type Brand = Pick<Schemas["Site"], "logo_url" | "api_base_url" | "brand_color"> & {
  name?: string;
  registration: Registration;
};

// What the page knows of its site: still asking, the call failed (asked again when the failure may pass), or the answer.
export type SiteState = { status: "pending" } | { status: "failed" } | { status: "ready"; brand: Brand };

// A failed call is asked again after a growing wait, up to this, and at once when the browser is back online.
const retryCap = 30_000;

// The site's last answer in this tab, kept so the home page come back to (from the sign-in pages, a reload) is drawn
// with its ways in from the first frame, not with them coming in a moment later and moving the page. It stands for an
// answer for this long, and the page asks again all the same.
const keptKey = "axiom:site";
const keptFor = 10 * 60_000;
function keep(site: Schemas["Site"]) {
  try {
    sessionStorage.setItem(keptKey, JSON.stringify({ at: Date.now(), site }));
  } catch {
    // storage refused: the next page waits for its own answer
  }
}
function kept(): SiteState {
  try {
    const { at, site } = JSON.parse(sessionStorage.getItem(keptKey) ?? "null") ?? {};
    if (typeof at === "number" && Date.now() - at < keptFor && site) return { status: "ready", brand: brandOf(site) };
  } catch {
    // storage refused, or not ours to read: as if nothing was kept
  }
  return { status: "pending" };
}

// The page's latest call for the site, answered or still out: the sign-in pages the website holds read the same answer
// (home/sign-in-host.tsx) rather than ask for it again.
let latest: Promise<Schemas["Site"]> | null = null;
export const siteAnswer = () => latest;

const brandOf = (site: Schemas["Site"]): Brand => ({
  name: site.name || undefined,
  logo_url: site.logo_url,
  api_base_url: site.api_base_url,
  registration: registrationOf(site),
  brand_color: site.brand_color,
});

// useSiteState asks for the site's name, logo, API address and how it takes new users, from the same public call the
// console's sign-in page uses, once for the page: what promises depend on the answer waits for it (or the tab's kept
// answer), a call that failed on the network or the server is asked again, and a page the browser brings back from its
// cache asks afresh, since the settings may have changed while it was away. The site's brand colour is put on the
// document's root (useBrandStyle, as the console's shells do), so every part of the page takes it. A host the API does
// not know leaves the default mark, name and colours.
export function useSiteState(): SiteState {
  const [state, setState] = useState<SiteState>(kept);
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
      latest = unwrap(consoleApi.GET("/api/console/v1/site", { signal: mine.signal }));
      latest.then(
        (site) => {
          if (mine.signal.aborted) return;
          failures = 0;
          keep(site);
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
