import Wide, { type Props } from "@axiom/boards/Site/regions/Trust";
import wideEn from "@axiom/boards/Site/regions/Trust.en";
import wideZh from "@axiom/boards/Site/regions/Trust.zh";
import Narrow from "@axiom/boards/SitePhone/regions/Trust";
import narrowEn from "@axiom/boards/SitePhone/regions/Trust.en";
import narrowZh from "@axiom/boards/SitePhone/regions/Trust.zh";
import { useTranslation } from "react-i18next";
import { screenVars, useBoardWords, useFitFrame, useScript } from "./script";
import { start } from "./trust-script";

const vars = screenVars("--okDot: var(--ok)", "--warnDot: var(--warn)", "--warnText: var(--warnT)");

const zh = { ...wideZh, ...narrowZh };
const en = { ...wideEn, ...narrowEn };

// What to settle before production (Site.dc.html M4): the official-only switch, the ledger and a key's ceiling, played
// on the board's own figures, which the lede says are sample data.
export function TrustScreen({ wide }: { wide: boolean }) {
  const { t } = useTranslation();
  const word = useBoardWords(zh, en, { m90a20e4359: ` ${t("script.trust.sample")}` });
  const { setStage, stage, values, entered } = useScript(start, { t: (w) => t(`script.trust.${w}`) });
  useFitFrame(stage, !wide);
  const Region = wide ? Wide : Narrow;
  return (
    <Region
      t={word}
      values={{ ...values, m4_vars: vars, m4_setStage: setStage, m4_in: entered ? "in" : "" } as Props["values"]}
      text={{ ledgerNote: t("script.trust.ledgerNote") }}
    />
  );
}
