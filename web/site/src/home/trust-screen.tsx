import Wide, { type Props } from "@axiom/boards/Site/regions/Trust";
import wideEn from "@axiom/boards/Site/regions/Trust.en";
import wideZh from "@axiom/boards/Site/regions/Trust.zh";
import Narrow from "@axiom/boards/SitePhone/regions/Trust";
import narrowEn from "@axiom/boards/SitePhone/regions/Trust.en";
import narrowZh from "@axiom/boards/SitePhone/regions/Trust.zh";
import { useTranslation } from "react-i18next";
import { screenVars, useBoardWords, useScript } from "./script";
import { start } from "./trust-script";

const vars = screenVars("--okDot: var(--ok)", "--warnDot: var(--warn)", "--warnText: var(--warnT)");

// What to settle before production (Site.dc.html M4): the official-only switch, the ledger and a key's ceiling, played
// on the board's own figures.
export function TrustScreen({ wide }: { wide: boolean }) {
  const { t } = useTranslation();
  const word = useBoardWords({ ...wideZh, ...narrowZh }, { ...wideEn, ...narrowEn });
  const { setStage, values } = useScript(start, { t: (w) => t(`script.trust.${w}`) });
  const Region = wide ? Wide : Narrow;
  return <Region t={word} values={{ ...values, m4_vars: vars, m4_setStage: setStage } as Props["values"]} />;
}
