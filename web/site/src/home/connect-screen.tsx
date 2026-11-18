import Wide, { type Props } from "@axiom/boards/Site/regions/Connect";
import wideEn from "@axiom/boards/Site/regions/Connect.en";
import wideZh from "@axiom/boards/Site/regions/Connect.zh";
import Narrow from "@axiom/boards/SitePhone/regions/Connect";
import narrowEn from "@axiom/boards/SitePhone/regions/Connect.en";
import narrowZh from "@axiom/boards/SitePhone/regions/Connect.zh";
import { useTranslation } from "react-i18next";
import { useApiAddress } from "./api-address";
import { start } from "./connect-script";
import { links } from "./links";
import { useScheme } from "./scheme";
import { screenVars, useBoardWords, useScript } from "./script";

// The machine's palette (the board's --anatomy-*) on the page's tokens, in the two schemes; its halo's opacity is a
// number, which follows the scheme here.
const vars = (scheme: "light" | "dark") =>
  screenVars(
    "--okc: var(--okT)",
    "--addBg: light-dark(color-mix(in srgb, var(--ok) 7%, transparent), color-mix(in srgb, var(--ok) 8%, transparent))",
    "--passBg: light-dark(color-mix(in srgb, var(--accentSolid) 16%, transparent), color-mix(in srgb, var(--accentSolid) 22%, transparent))",
    "--dispBg: light-dark(#0d1c21, #0b161a)",
    "--dispInk: #4ccce6",
    "--dispDim: #2b8aa0",
    "--chipBg: var(--accentWash)",
    "--chipHover: color-mix(in oklab, var(--accentSolid) 18%, var(--n1))",
    "--anatomy-paper: light-dark(var(--n1), var(--n2))",
    "--anatomy-face: light-dark(var(--n1), var(--n3))",
    "--anatomy-top: light-dark(var(--n1), var(--n5))",
    "--anatomy-shade-0: light-dark(var(--n5), var(--n1))",
    "--anatomy-shade-1: light-dark(var(--n4), var(--n2))",
    "--anatomy-shade-2: var(--n3)",
    "--anatomy-shade-3: light-dark(var(--n2), var(--n4))",
    "--anatomy-lit-top: light-dark(var(--accentWash), color-mix(in oklab, var(--accentSolid) 18%, var(--n1)))",
    "--anatomy-lit-shade: light-dark(var(--accentSoft), var(--accentWash))",
    "--anatomy-hi: light-dark(var(--n12), var(--n11))",
    "--anatomy-mid: light-dark(var(--n9), var(--n8))",
    "--anatomy-lo: light-dark(var(--n8), var(--n7))",
    "--anatomy-faint: light-dark(var(--n6), var(--n5))",
    "--anatomy-lit: light-dark(var(--accentSolid), var(--accentT))",
    "--anatomy-dot: light-dark(var(--n11), var(--n12))",
    "--anatomy-halo: light-dark(var(--n9), #000000)",
    `--anatomy-halo-opacity: ${scheme === "dark" ? 0.85 : 0.3}`,
    "--anatomy-weight: var(--bw)",
  );

const zh = { ...wideZh, ...narrowZh };
const en = { ...wideEn, ...narrowEn };

// The connect screen (Site.dc.html M1): a tool's configuration with the site's address in it, and the machine that
// plugs into it and sends a call to a vendor, as the board draws them. onWhale is called when the card names a DeepSeek
// model, the whale's chance meeting.
export function ConnectScreen({ wide, onWhale }: { wide: boolean; onWhale: () => void }) {
  const { t } = useTranslation();
  const scheme = useScheme();
  const { known, root, v1 } = useApiAddress();
  const word = useBoardWords(zh, en);
  const { setStage, values, entered } = useScript(
    (stage: HTMLElement, changed: () => void) => start(stage, changed, !wide, onWhale),
    { word: (w) => t(`script.connect.${w}`), root, v1, known },
  );
  const Region = wide ? Wide : Narrow;
  return (
    <Region
      t={word}
      values={
        { ...values, m1_vars: vars(scheme), m1_setStage: setStage, m1_in: entered ? "in" : "" } as Props["values"]
      }
      text={Object.fromEntries(Array.from({ length: 9 }, (_, i) => [`signUpHref${i}`, links.signUp])) as Props["text"]}
    />
  );
}
