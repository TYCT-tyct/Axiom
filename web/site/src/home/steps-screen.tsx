import Wide, { type Props } from "@axiom/boards/Site/regions/Steps";
import wideEn from "@axiom/boards/Site/regions/Steps.en";
import wideZh from "@axiom/boards/Site/regions/Steps.zh";
import Narrow from "@axiom/boards/SitePhone/regions/Steps";
import narrowEn from "@axiom/boards/SitePhone/regions/Steps.en";
import narrowZh from "@axiom/boards/SitePhone/regions/Steps.zh";
import { useRef } from "react";
import { useTranslation } from "react-i18next";
import { links } from "./links";
import { screenVars, useBoardWords, useScript } from "./script";
import { useSite } from "./site-context";
import { start } from "./steps-script";

const vars = screenVars(
  "--okDot: var(--ok)",
  "--okText: var(--okT)",
  "--addBg: light-dark(color-mix(in srgb, var(--ok) 10%, transparent), color-mix(in srgb, var(--ok) 12%, transparent))",
  "--addText: var(--okT)",
  "--delBg: light-dark(color-mix(in srgb, var(--bad) 8%, transparent), color-mix(in srgb, var(--bad) 12%, transparent))",
  "--delText: var(--badT)",
);

const zh = { ...wideZh, ...narrowZh };
const en = { ...wideEn, ...narrowEn };

// The three steps from sign-up to the first request (Site.dc.html M3), played on the board's own figures, which the
// lede says are sample data.
export function StepsScreen({ wide }: { wide: boolean }) {
  const { t } = useTranslation();
  const word = useBoardWords(zh, en, { mac86e0f670: ` ${t("script.steps.sample")}` });
  // the base_url the reader is shown is the site's API address, which arrives after the script starts
  const api = useSite().brand?.api_base_url;
  const url = useRef("");
  url.current = api ? `${api.replace(/\/+$/, "")}/v1` : "";
  const { setStage, values, entered } = useScript(
    (stage: HTMLElement, changed: () => void) => start(stage, changed, () => url.current),
    { t: (w) => t(`script.steps.${w}`) },
  );
  const Region = wide ? Wide : Narrow;
  return (
    <Region
      t={word}
      values={{ ...values, m3_vars: vars, m3_setStage: setStage, m3_in: entered ? "in" : "" } as Props["values"]}
      text={{ signUpHref: links.signUp }}
    />
  );
}
