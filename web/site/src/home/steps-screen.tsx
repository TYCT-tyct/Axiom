import Wide, { type Props } from "@axiom/boards/Site/regions/Steps";
import wideEn from "@axiom/boards/Site/regions/Steps.en";
import wideZh from "@axiom/boards/Site/regions/Steps.zh";
import Narrow from "@axiom/boards/SitePhone/regions/Steps";
import narrowEn from "@axiom/boards/SitePhone/regions/Steps.en";
import narrowZh from "@axiom/boards/SitePhone/regions/Steps.zh";
import { useTranslation } from "react-i18next";
import { links } from "./links";
import { screenVars, useBoardWords, useScript } from "./script";
import { start } from "./steps-script";

const vars = screenVars(
  "--okDot: var(--ok)",
  "--okText: var(--okT)",
  "--addBg: light-dark(color-mix(in srgb, var(--ok) 10%, transparent), color-mix(in srgb, var(--ok) 12%, transparent))",
  "--addText: var(--okT)",
  "--delBg: light-dark(color-mix(in srgb, var(--bad) 8%, transparent), color-mix(in srgb, var(--bad) 12%, transparent))",
  "--delText: var(--badT)",
);

// The three steps from sign-up to the first request (Site.dc.html M3), played on the board's own figures.
export function StepsScreen({ wide }: { wide: boolean }) {
  const { t } = useTranslation();
  const word = useBoardWords({ ...wideZh, ...narrowZh }, { ...wideEn, ...narrowEn });
  const { setStage, values } = useScript(start, { t: (w) => t(`script.steps.${w}`) });
  const Region = wide ? Wide : Narrow;
  return (
    <Region
      t={word}
      values={{ ...values, m3_vars: vars, m3_setStage: setStage } as Props["values"]}
      text={{ signUpHref: links.signUp }}
    />
  );
}
