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
import { useSite } from "./site-context";

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
const illustrated = [0, 1, 3, 4, 5, 6];

// The questions people ask first (Site.dc.html M5), each with its small picture: the answers are the page's (script.faq),
// true for this site's settings, and the addresses in the picture of the tools' settings are the site's.
export function FaqScreen({ wide }: { wide: boolean }) {
  const { t } = useTranslation();
  const word = useBoardWords(zh, en);
  const scheme = useScheme();
  const { root, v1 } = useApiAddress();
  const { brand, name } = useSite();
  const site = { site: name };
  const said = [
    t("script.faq.a0", site),
    t("script.faq.a1"),
    t("script.faq.a2"),
    t("script.faq.a3"),
    t("script.faq.a4", site),
    t("script.faq.a5"),
    t("script.faq.a6"),
    t(`script.faq.a7.${brand?.registration ?? "open"}`),
  ];
  const answer = (i: number) => {
    const text = said[i] as string;
    return illustrated.includes(i) ? `${text} ${t("script.faq.sample")}` : text;
  };
  const { setStage, values, entered } = useScript(
    (stage: HTMLElement, changed: () => void) => start(stage, changed, !wide),
    {},
  );
  const text: Props["text"] = {
    ans0: answer(0),
    ans1: answer(1),
    ans2: answer(2),
    ans3: answer(3),
    ans4: answer(4),
    ans5: answer(5),
    ans6: answer(6),
    ans7: answer(7),
    apiRoot: root,
    apiV1: v1,
    billing: t("script.faq.billing"),
  };
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
      text={text}
    />
  );
}
