import Wide, { type Props } from "@axiom/boards/Site/regions/Faq";
import wideEn from "@axiom/boards/Site/regions/Faq.en";
import wideZh from "@axiom/boards/Site/regions/Faq.zh";
import Narrow from "@axiom/boards/SitePhone/regions/Faq";
import narrowEn from "@axiom/boards/SitePhone/regions/Faq.en";
import narrowZh from "@axiom/boards/SitePhone/regions/Faq.zh";
import { useTranslation } from "react-i18next";
import { useApiAddress } from "./api-address";
import { resting, start } from "./faq-script";
import { useScheme } from "./scheme";
import { screenVars, useBoardWords, useScript } from "./script";

// The board's cable is a little fainter in the dark; an opacity is not a colour, so it follows the scheme here.
const vars = (scheme: "light" | "dark") =>
  screenVars(
    "--dotc: light-dark(var(--n7), var(--n6))",
    "--glow: drop-shadow(0 0 3px light-dark(transparent, var(--accentSolid)))",
    `--trO: ${scheme === "dark" ? 0.6 : 0.9}`,
    "--winShadow: 0 8px 24px light-dark(rgba(0, 0, 0, 0.06), rgba(0, 0, 0, 0.4))",
  );

const zh = { ...wideZh, ...narrowZh };
const en = { ...wideEn, ...narrowEn };
// The answers whose picture shows prices, bills or keys: the figures there are the board's illustration.
const illustrated = ["m0a6b58d599", "m7908066605", "m59549165d5", "m9799ce84e2", "m475efcf60a"] as const;

// The questions people ask first (Site.dc.html M5): the board's own answers, each with its small picture. The addresses
// in the picture of the tools' settings are the site's.
export function FaqScreen({ wide }: { wide: boolean }) {
  const { t } = useTranslation();
  const word = useBoardWords(zh, en, Object.fromEntries(illustrated.map((key) => [key, ` ${t("script.faq.sample")}`])));
  const scheme = useScheme();
  const { root, v1 } = useApiAddress();
  const { setStage, values, entered } = useScript(
    (stage: HTMLElement, changed: () => void) => start(stage, changed, !wide),
    {},
  );
  const Region = wide ? Wide : Narrow;
  return (
    <Region
      t={word}
      values={
        {
          ...resting,
          ...values,
          m5_vars: vars(scheme),
          m5_setStage: setStage,
          m5_in: entered ? "in" : "",
        } as Props["values"]
      }
      text={{ apiRoot: root, apiV1: v1 }}
    />
  );
}
