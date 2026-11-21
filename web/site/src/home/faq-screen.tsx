import Wide, { type Props } from "@axiom/boards/Site/regions/Faq";
import wideEn from "@axiom/boards/Site/regions/Faq.en";
import wideZh from "@axiom/boards/Site/regions/Faq.zh";
import Narrow from "@axiom/boards/SitePhone/regions/Faq";
import narrowEn from "@axiom/boards/SitePhone/regions/Faq.en";
import narrowZh from "@axiom/boards/SitePhone/regions/Faq.zh";
import { useTranslation } from "react-i18next";
import { useApiAddress } from "./api-address";
import styles from "./faq-screen.module.css";
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
// The site's name in the first picture's box, which holds about nine Latin letters, a CJK character counting as two: a
// longer name keeps its start, enough to know it by, and ends in an ellipsis.
const room = 9;
const width = (ch: string) => (/[\u2e80-\uffff]/u.test(ch) ? 2 : 1);
function fitted(name: string) {
  const chars = [...name];
  if (chars.reduce((sum, ch) => sum + width(ch), 0) <= room) return name;
  let used = 1;
  let kept = "";
  for (const ch of chars) {
    used += width(ch);
    if (used > room) break;
    kept += ch;
  }
  return `${kept.trimEnd()}…`;
}

// The questions people ask first (Site.dc.html M5), each with its small picture: the answers are the page's (script.faq),
// true for this site's settings, and the addresses in the picture of the tools' settings are the site's.
export function FaqScreen({ wide }: { wide: boolean }) {
  const { t } = useTranslation();
  const word = useBoardWords(zh, en);
  const scheme = useScheme();
  const { root, v1, known } = useApiAddress();
  const { name, join } = useSite();
  const site = { site: name };
  const { setStage, values, entered } = useScript(
    (stage: HTMLElement, changed: () => void) => start(stage, changed, !wide),
    {},
  );
  const text: Props["text"] = {
    ans0: t("script.faq.a0", site),
    ans1: t("script.faq.a1"),
    ans2: t("script.faq.a2"),
    ans3: t("script.faq.a3"),
    ans4: t("script.faq.a4", site),
    ans5: t("script.faq.a5"),
    ans6: t("script.faq.a6"),
    ans7: t(`script.faq.a7.${join}`),
    apiRoot: root,
    apiV1: v1,
    billing: t("script.faq.billing"),
    signUp: t(`script.faq.signUp.${join}`),
    envNote: known ? t("script.faq.envNote") : t("script.noApi"),
    fanName: fitted(name),
  };
  const Region = wide ? Wide : Narrow;
  return (
    <div className={styles.faq}>
      <Region
        t={word}
        values={
          {
            ...resting,
            ...values,
            m5_vars: vars(scheme),
            // without an address the tools' picture shows no command, only that it is unavailable
            m5_noApi: !known,
            m5_setStage: setStage,
            m5_in: entered ? "in" : "",
            // the label on the pictures whose prices, bills and keys are the board's illustration
            m5_note: t("script.faq.sample"),
          } as Props["values"]
        }
        text={text}
      />
    </div>
  );
}
