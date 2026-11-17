import type { Region } from "../regions.ts";

// The website's frame and first screen (#747 S1), in the wide layout (Site) and the narrow one (SitePhone, whose markup
// and CSS the two tablet boards repeat byte for byte). The frame keeps the header, the theme toggle, the language
// panel, the island and the tour: they sit beside the scroller the screens go in, and a region has one slot. The first
// screen is the same in both layouts, so it is cut once.
// The English of the frame's and the first screen's words, by locale key. Where prefs.js also writes a text (its COPY
// and UI tables), this is its wording, so the page and the engine never disagree.
const siteEnglish: Record<string, string> = {
  m9bae680405: "Main navigation",
  m217cfe7db1: "Following system",
  m4ce4cafdd0: "Menu",
  mc98e118e0a: "Models",
  m0f4371a7a9: "Pricing",
  m1e2df9c307: "Log in",
  mc4fb62202b: "Sign up",
  m3272fe53c0: "Take the tour",
  mb90a8ac9c4: "Choose a language",
  m3fd47edce4: "Close",
  m4144869c18: "Search languages",
  m9937e365aa: "简体中文",
  mcaf724c6bd: "Sign up and get an API key",
  m2e2a3b1e5d: "One key for every model",
  m0d0cee5072: "Every route is tested. You pay for the route that served you.",
};
const menuLinks = (anchors: string) =>
  Object.fromEntries(
    ["menuModelsHref", "menuPricingHref", "menuSignInHref"].map((name, i) => [
      name,
      { selector: `${anchors}:nth-child(${i + 1})`, attribute: "href" },
    ]),
  );
// What has nowhere to go yet: the docs page comes with S4, the screens below the first (the menu sheet's sections and
// the tour through them) with S2. They are cut out until then; the links that remain close up in the board's order.
const notYet = [
  ".bar .nav.mid a:nth-child(3)",
  ".isl .ixi a:nth-child(3)",
  ".sheet .shl a:nth-child(3)",
  ".sheet .shs",
  ".tr .tour",
];
// The site's own logo and name go in the brand's places (docs/frontend.md「主题与自由度」): the page fills them, with the
// board's mark (the Mark region) when the site has no logo or it fails to load.
const brand = [".bar .brand > *", ".sheet .brand > *", ".isl .im > *"];
const otherLanguages = ["zh-TW", "ja", "ko", "es", "de", "fr", "pt", "ru"].map((code) => `#lp-${code}`);
const siteRegions: Region[] = [
  ...["Site", "SitePhone"].map(
    (board): Region => ({
      board,
      name: "Shell",
      roots: [".tr.site"],
      // the lower screens' canvases, the sign-in page and the transition come with S2 and S3; the site has two languages
      omit: [
        ".tr > .cw",
        ".tr > .wh",
        ".tr > .lg",
        ".tr > .fly",
        ".tr > canvas.ui",
        ...notYet,
        ...brand,
        ...otherLanguages,
      ],
      children: ".hs",
      text: {
        models: ".bar .nav.mid a:nth-child(1) > span",
        pricing: ".bar .nav.mid a:nth-child(2) > span",
        language: ".bar .lang [data-i18n]",
        signIn: ".bar .login > span",
        signUp: ".bar .btn.primary > span",
      },
      attributes: {
        home: { selector: ".bar .brand", attribute: "href" },
        islandLabel: { selector: ".isl .im", attribute: "aria-label" },
        modelsHref: { selector: ".bar .nav.mid a:nth-child(1)", attribute: "href" },
        pricingHref: { selector: ".bar .nav.mid a:nth-child(2)", attribute: "href" },
        signInHref: { selector: ".bar .login", attribute: "href" },
        signUpHref: { selector: ".bar .btn.primary", attribute: "href" },
        // the same pages again in the island (wide) or in the menu sheet (narrow)
        ...menuLinks(board === "Site" ? ".isl .ixi a" : ".sheet .shl a"),
        menuSignUpHref: { selector: ".isl .btn", attribute: "href" },
        ...(board === "Site" ? {} : { sheetSignUpHref: { selector: ".sheet .shf a", attribute: "href" } }),
      },
      classes: {
        modeCls: ["dark", "light"],
        barCls: ["away"],
        islCls: ["on", "hide", "open"],
        sheetCls: ["on"],
      },
      english: siteEnglish,
    }),
  ),
  {
    board: "Site",
    name: "FirstScreen",
    roots: [".hs > main"],
    omit: [".cta .outline"],
    children: "h1 > [data-i18n]",
    text: {
      lede: ".lede > span",
      signUp: ".cta .primary > span",
    },
    attributes: {
      signUpHref: { selector: ".cta .primary", attribute: "href" },
    },
    english: siteEnglish,
  },
  { board: "Site", name: "Mark", roots: [".bar .brand > svg"] },
  {
    board: "Site",
    name: "Headline",
    roots: ["h1 > [data-i18n] > .ph"],
  },
];

/** @public loaded by regions.ts from the directory listing */
export default siteRegions;
