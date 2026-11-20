import Wide from "@axiom/boards/Site/regions/Shell";
import Narrow from "@axiom/boards/SitePhone/regions/Shell";
import { useEffectModule } from "@axiom/fx";
import type { Sea } from "@axiom/fx/sea";
import { type ReactNode, type RefObject, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useDeparture, useFrameBehaviour } from "./behaviour";
import { BrandPlaces } from "./brand";
import { Eggs } from "./eggs";
import { links } from "./links";
import { useScheme } from "./scheme";
import { WhaleContext } from "./screens";
import { type Join, useSite } from "./site-context";
import { useSwitches } from "./switches";
import { useWhale } from "./whale";
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

// The frame's words the switches' engine rewrites, in the language the page opened in, where its links go, and the
// site's name for the island's mark.
const frameText = (word: (key: BoardKey) => string, language: string, name: string) => ({
  models: word(words.models),
  pricing: word(words.pricing),
  language,
  signIn: word(words.signIn),
  signUp: word(words.signUp),
  home: links.home,
  islandLabel: name,
  modelsHref: links.models,
  pricingHref: links.pricing,
  signInHref: links.signIn,
  signUpHref: links.signUp,
  menuModelsHref: links.models,
  menuPricingHref: links.pricing,
  menuSignInHref: links.signIn,
  menuSignUpHref: links.signUp,
  sheetSignUpHref: links.signUp,
});

// From 700 wide the narrow layout is laid out for a tablet: the board's script marks its .ho data-tab (the steps and
// the trust screens take their side-by-side form, the connect screen drops its cable).
function useTabletLayout(root: RefObject<HTMLElement | null>, wide: boolean) {
  useEffect(() => {
    const frame = root.current;
    const ho = frame?.querySelector(".ho");
    if (!frame || !ho || wide) return;
    const mark = () => ho.toggleAttribute("data-tab", frame.clientWidth >= 700);
    mark();
    const resized = new ResizeObserver(mark);
    resized.observe(frame);
    return () => resized.disconnect();
  }, [root, wide]);
}

// The header's, the island's, the menu's and the first screen's ways to register are offered only once the site has said
// it takes new users: a site that takes nobody new, or one that has not answered yet, offers signing in (the screens
// below lead there instead, links.ts joinHref). Their words are the switches' engine's, which writes "register" into
// them whatever the page says, so they are taken out rather than reworded.
function useRegistration(root: RefObject<HTMLElement | null>, join: Join) {
  const closed = join === "closed" || join === "unknown";
  useLayoutEffect(() => {
    for (const way of root.current?.querySelectorAll<HTMLElement>(`a[href="${links.signUp}"]:not(.under *)`) ?? [])
      way.style.display = closed ? "none" : "";
  }, [root, closed]);
}

// The tour's button is offered at the top, until the header turns solid; a button that is not offered is not in the
// keyboard's way either.
function useTour(root: RefObject<HTMLElement | null>, gone: boolean) {
  useLayoutEffect(() => {
    root.current?.querySelector(".tour")?.toggleAttribute("inert", gone);
  }, [root, gone]);
  return gone ? "gone" : "";
}

// SiteFrame is the home page's frame as the board draws it: the sea, the header with the language and theme switches,
// and the island, around the scroller children go in. The board's behaviours run on it (behaviour.ts), and
// its engines through packages/fx: the sea after the first content, the switches when first used (switches.ts), the
// departure to the console's sign-in pages when a [data-go] link is first pressed (behaviour.ts).
//
// The switches' engine rewrites the header's and the first screen's words in place, decoding them into the chosen
// language. So React writes those words once, in the language the page opened in, and never again: they are the
// regions' text props, fixed at mount. The other words follow the language as usual.
export function SiteFrame({ wide, met, children }: { wide: boolean; met: RefObject<boolean>; children: ReactNode }) {
  const { t, i18n } = useTranslation();
  const scheme = useScheme();
  const [opened] = useState(() => i18n.getFixedT(i18n.language));
  const root = useRef<HTMLElement | null>(null);
  const setCanvas = useCallback((canvas: Element | null) => {
    root.current = canvas?.closest<HTMLElement>(".tr") ?? null;
  }, []);
  // the sea, and once it is up the eggs that play on it
  const [seaUp, setSeaUp] = useState<Sea | null>(null);
  const sea = useEffectModule(root, loadSea, (module, frame) => {
    const started = module.startSea(frame, frame.querySelector(".hs > main") ?? frame, scheme === "light");
    setSeaUp(started);
    return started;
  });
  useSwitches(root, sea);
  useDeparture(root, sea);
  const whale = useWhale(root, sea, met, { narrow: !wide, dark: scheme === "dark" });
  const { scroll, island, menu } = useFrameBehaviour(root, wide);
  useTabletLayout(root, wide);
  const { brand, name, join } = useSite();

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

  const tourCls = useTour(root, scroll.solid);

  useRegistration(root, join);
  const board = (key: BoardKey) => t(`board.${key}`);
  const values = {
    modeCls: scheme,
    vars: frameVars,
    setCanvas,
    barCls: scroll.solid ? "away" : "",
    islCls: scroll.solid ? (scroll.bottom ? "on hide" : `on${island ? " open" : ""}`) : "",
    secTxt: t(`sections.${sections[scroll.sec]?.name ?? "surface"}`),
    depthTxt: depthText(scroll.depth),
    sheetCls: menu ? "on" : "",
    sheetHide: !menu,
    tourCls,
    ...Object.fromEntries(sections.map((_, i) => [`t${i}`, i === scroll.sec ? "on" : ""])),
  };
  const text = frameText((key) => opened(`board.${key}`), opened("language"), name);
  const Shell = wide ? Wide : Narrow;
  return (
    <>
      <WhaleContext value={whale.meet}>
        <Shell t={board} values={values} text={text}>
          {children}
        </Shell>
      </WhaleContext>
      <BrandPlaces root={root} logoUrl={brand?.logo_url} name={name} />
      <Eggs root={root} sea={seaUp} whale={whale.call} />
    </>
  );
}
