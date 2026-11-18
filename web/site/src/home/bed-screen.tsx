import Wide, { type Props } from "@axiom/boards/Site/regions/Bed";
import wideEn from "@axiom/boards/Site/regions/Bed.en";
import wideZh from "@axiom/boards/Site/regions/Bed.zh";
import Narrow from "@axiom/boards/SitePhone/regions/Bed";
import narrowEn from "@axiom/boards/SitePhone/regions/Bed.en";
import narrowZh from "@axiom/boards/SitePhone/regions/Bed.zh";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { useApiAddress } from "./api-address";
import { start } from "./bed-script";
import { links } from "./links";
import { useScheme } from "./scheme";
import { screenVars, useBoardWords, useScript } from "./script";

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
  useEffect(() => script?.retheme(), [script, scheme]);
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
  return (
    <Region
      t={word}
      values={v as Props["values"]}
      text={{
        credit: t("footer"),
        apiV1: v1,
        signUpHref: links.signUp,
        homeHref: links.home,
        modelsHref: links.models,
        pricingHref: links.pricing,
      }}
    />
  );
}
