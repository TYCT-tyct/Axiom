import type { Region } from "../regions.ts";

// The website's frame and first screen (#747 S1), in the wide layout (Site) and the narrow one (SitePhone, whose markup
// and CSS the two tablet boards repeat byte for byte). The frame keeps the header, the theme toggle, the language
// panel, the island and the tour: they sit beside the scroller the screens go in, and a region has one slot. The first
// screen is the same in both layouts, so it is cut once.
// The English of the frame's and the first screen's words, by locale key. Where prefs.js also writes a text (its COPY
// and UI tables), this is its wording, so the page and the engine never disagree.
const siteEnglish: Record<string, string> = {
  m2687ccdbb1: "Docs",
  md006830a9e: "Read the docs",
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
  m854975fac5: "On this page",
  m6ebdfb798f: "Surface",
  mf3b3924597: "Connect",
  m446d270979: "Models and routes",
  mc7765bcffd: "Three steps",
  m97d3aaa40b: "Before production",
  m45a6d115fd: "FAQ",
  m8c0f8088c6: "Seabed",
};
const menuLinks = (anchors: string) =>
  Object.fromEntries(
    // the third link is the docs', reserved until the docs page comes (#816)
    [
      ["menuModelsHref", 1],
      ["menuPricingHref", 2],
      ["menuSignInHref", 4],
    ].map(([name, nth]) => [name, { selector: `${anchors}:nth-child(${nth})`, attribute: "href" }]),
  );
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
      // the sign-in page and the transition are the console's (S3); the site has two languages
      omit: [".tr > .lg", ".tr > .fly", ".tr > canvas.ui", ...brand, ...otherLanguages],
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
        tourCls: ["gone"],
      },
      english: siteEnglish,
    }),
  ),
  {
    board: "Site",
    name: "FirstScreen",
    roots: [".hs > main"],
    children: "h1 > [data-i18n]",
    // the strip's two names light when the whale or Clawd answers them (whale-egg.js, clawd-cast.js)
    states: ["wh-lit", "cw-lit"],
    // Clawd's lit term uses the contrast-checked token (4.5:1 in light, board's #d97757 in dark)
    css: { ".ho .main .fam:not(.under *) > span.cw-lit": "color:var(--clawdT)" },
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
