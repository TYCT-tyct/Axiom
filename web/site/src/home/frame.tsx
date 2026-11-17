import Wide from "@axiom/boards/Site/regions/Shell";
import Narrow from "@axiom/boards/SitePhone/regions/Shell";
import { useEffectModule } from "@axiom/fx";
import { type ReactNode, useCallback, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useFrameBehaviour } from "./behaviour";
import { links } from "./links";
import { useScheme } from "./scheme";
import { useSwitches } from "./switches";
import { type BoardKey, sections, words } from "./words";

const loadSea = () => import("@axiom/fx/sea");

// The board's script gives the frame these from its own palette (themeOf); here they are the tokens they stand for.
// --fw is the frame's width, which the wide layout's margins follow.
const frameVars = [
  "--panel: var(--n2)",
  "--accent: var(--accentT)",
  "--veilStrong: light-dark(color-mix(in srgb, var(--n1) 78%, transparent), color-mix(in srgb, var(--n1) 72%, transparent))",
  "--fw: 100vw",
].join("; ");

const depthText = (depth: number) => (depth === 0 ? "0 m" : `−${depth.toLocaleString("en-US")} m`);

// The frame's words the switches' engine rewrites, in the language the page opened in, and where its links go.
const frameText = (word: (key: BoardKey) => string, language: string) => ({
  models: word(words.models),
  pricing: word(words.pricing),
  docs: word(words.docs),
  language,
  signIn: word(words.signIn),
  signUp: word(words.signUp),
  home: links.home,
  modelsHref: links.models,
  pricingHref: links.pricing,
  docsHref: links.docs,
  signInHref: links.signIn,
  signUpHref: links.signUp,
  menuModelsHref: links.models,
  menuPricingHref: links.pricing,
  menuDocsHref: links.docs,
  menuSignInHref: links.signIn,
  menuSignUpHref: links.signUp,
  sheetSignUpHref: links.signUp,
});

// SiteFrame is the home page's frame as the board draws it: the sea, the header with the language and theme switches,
// the island and the tour, around the scroller children go in. The board's behaviours run on it (behaviour.ts), and
// its engines through packages/fx: the sea after the first content, the switches when first used (switches.ts).
//
// The switches' engine rewrites the header's and the first screen's words in place, decoding them into the chosen
// language. So React writes those words once, in the language the page opened in, and never again: they are the
// regions' text props, fixed at mount. The other words follow the language as usual.
export function SiteFrame({ wide, children }: { wide: boolean; children: ReactNode }) {
  const { t, i18n } = useTranslation();
  const scheme = useScheme();
  const [opened] = useState(() => i18n.getFixedT(i18n.language));
  const root = useRef<HTMLElement | null>(null);
  const setCanvas = useCallback((canvas: Element | null) => {
    root.current = canvas?.closest<HTMLElement>(".tr") ?? null;
  }, []);
  const sea = useEffectModule(root, loadSea, (module, frame) =>
    module.startSea(frame, frame.querySelector(".hs > main") ?? frame, scheme === "light"),
  );
  useSwitches(root, sea);
  const { scroll, island, menu } = useFrameBehaviour(root, wide);

  // What the switches' engine sets, until it is loaded: it starts from them, and keeps them the same itself.
  const language = i18n.language === "en" ? "en" : "zh-CN";
  const themeLabel = scheme === "dark" ? t("theme.toLight") : t("theme.toDark");
  useLayoutEffect(() => {
    const frame = root.current;
    if (!frame) return;
    frame.lang = language;
    frame.dataset.eff = scheme;
    frame.querySelector(".bar .theme")?.setAttribute("aria-label", themeLabel);
    sea.current?.engine.setLight(scheme === "light");
  }, [language, scheme, themeLabel, sea]);

  const board = (key: BoardKey) => t(`board.${key}`);
  const values = {
    modeCls: scheme,
    vars: frameVars,
    setCanvas,
    barCls: scroll.solid ? "away" : "",
    islCls: scroll.solid ? (scroll.bottom ? "on hide" : `on${island ? " open" : ""}`) : "",
    secTxt: board(sections[scroll.sec]?.name ?? "m6ebdfb798f"),
    depthTxt: depthText(scroll.depth),
    tourCls: scroll.solid ? "gone" : "",
    sheetCls: menu ? "on" : "",
    sheetHide: menu ? "false" : "true",
    ...Object.fromEntries(sections.map((_, i) => [`t${i}`, i === scroll.sec ? "on" : ""])),
  };
  const text = frameText((key) => opened(`board.${key}`), opened("language"));
  return wide ? (
    <Wide t={board} values={values} text={text}>
      {children}
    </Wide>
  ) : (
    <Narrow t={board} values={values} text={text}>
      {children}
    </Narrow>
  );
}
