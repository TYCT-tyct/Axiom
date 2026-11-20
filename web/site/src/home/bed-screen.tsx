import Wide, { type Props } from "@axiom/boards/Site/regions/Bed";
import wideEn from "@axiom/boards/Site/regions/Bed.en";
import wideZh from "@axiom/boards/Site/regions/Bed.zh";
import Narrow from "@axiom/boards/SitePhone/regions/Bed";
import narrowEn from "@axiom/boards/SitePhone/regions/Bed.en";
import narrowZh from "@axiom/boards/SitePhone/regions/Bed.zh";
import { useContext, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { useApiAddress } from "./api-address";
import styles from "./bed-screen.module.css";
import { start } from "./bed-script";
import { Brand } from "./brand";
import { FloorContext } from "./clawd";
import { joinHref, links, pricesHref } from "./links";
import { usePrices } from "./public-models";
import { useScheme } from "./scheme";
import { screenVars, useBoardWords, useScript } from "./script";
import { useSite } from "./site-context";

const vars = screenVars(
  "--artInk: light-dark(var(--n12), var(--n11))",
  "--artSand: var(--n9)",
  "--artTile: light-dark(var(--n8), var(--n7))",
);

const zh = { ...wideZh, ...narrowZh };
const en = { ...wideEn, ...narrowEn };

// The sea bed (Site.dc.html M6): the page's closing words over a dune of glyphs, and the page's footer under them. The
// footer's language and appearance switches are the header's, from down here (switches.ts); the screen only shows which
// choice is made. The credit for the Chinese font is the footer's own line (docs/frontend.md「字体」).
export function BedScreen({ wide }: { wide: boolean }) {
  const { t, i18n } = useTranslation();
  const scheme = useScheme();
  const english = i18n.language === "en";
  const { v1 } = useApiAddress();
  const word = useBoardWords(zh, en);
  const { setStage, script, values, entered } = useScript(
    (stage: HTMLElement, changed: () => void) => start(stage, changed, !wide),
    undefined,
  );
  const { brand, name, join } = useSite();
  const prices = usePrices();
  // the dune takes the page's colours and the site's name: after the frame that applies a new brand colour, which the
  // home page puts on the root above this screen (shell.tsx useSiteState)
  useEffect(() => {
    const frame = requestAnimationFrame(() => script?.retheme());
    return () => cancelAnimationFrame(frame);
  }, [script, scheme, brand?.brand_color]);
  useEffect(() => script?.rename(name), [script, name]);
  // Clawd lives behind the dune's letters
  const floor = useContext(FloorContext);
  useEffect(() => {
    if (!script) return;
    floor(script.art());
    return () => floor(null);
  }, [script, floor]);
  // a site that takes nobody new, or has not said yet, is entered by signing in
  const way = join === "open" || join === "invite" ? join : "signIn";
  const on = (yes: boolean) => (yes ? "on" : "");
  const v = {
    ...values,
    m6_vars: vars,
    m6_setStage: setStage,
    m6_in: entered ? "in" : "",
    m6_zhOn: on(!english),
    m6_enOn: on(english),
    m6_sunOn: on(scheme === "light"),
    m6_moonOn: on(scheme === "dark"),
    m6_zhP: !english,
    m6_enP: english,
    m6_sunP: scheme === "light",
    m6_moonP: scheme === "dark",
  };
  const Region = wide ? Wide : Narrow;
  // the board's footer, amended where its narrow layout would cut the site's words (bed-screen.module.css)
  return (
    <div className={styles.bed}>
      <Region
        t={word}
        values={v as Props["values"]}
        text={{
          credit: t("footer"),
          apiV1: v1,
          lede: t(`script.bed.lede.${way}`),
          join: t(`script.bed.join.${way}`),
          tagline: t("script.bed.tagline"),
          signUpHref: joinHref(join),
          homeHref: links.home,
          modelsHref: links.models,
          pricingHref: pricesHref(prices),
        }}
        // the footer's brand is the site's own, as the header's is (brand.tsx)
        brand={<Brand logoUrl={brand?.logo_url} name={name} />}
      />
    </div>
  );
}
