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
