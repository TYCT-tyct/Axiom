export type Region = {
  board: string;
  name: string;
  roots: string[];
  page?: string;
  omit?: string[];
  children?: string;
  repeats?: Record<string, string>;
  text?: Record<string, string>;
  attributes?: Record<string, { selector: string; attribute: string }>;
  classes?: Record<string, string[]>;
  english?: Record<string, string>;
};

// Only the page card adds regions. Selectors refer to the pinned design, never to generated JSX.
const consoleRegions: Region[] = ["AxApp", "AxAppPhone"].flatMap((board): Region[] => [
  {
    board,
    name: "Shell",
    roots: [".board"],
    omit: [".qdrs", ".qdr", ".kov", ".tst"],
    children: ".qpages",
    text: {
      workspaceSpent: ".qwsr .pmt > span",
      workspaceName: ".qwsr .pmt > b",
      ...(board === "AxApp"
        ? {
            accountName: ".qspm .qspi:first-child .pmt > span",
            personalBalance: ".gbal .sxP",
            teamBalance: ".gbal .sxT",
            spaceBalance: ".qspb",
            balanceWhole: ".bpv",
            balanceFraction: ".bpv .ct",
            balanceRunway: ".bp .bpt",
            balanceDate: ".bp .bpt > b",
            autoRecharge: ".bp .bpr .pmt > span",
            teamMembers: ".qspm .qspi:nth-child(2) .pmt > span",
          }
        : { spaceBalance: ".qswc .num" }),
    },
  },
  {
    board,
    name: "Overview",
    roots: [".p-ov"],
    page: "ov",
    repeats: {
      activity: ".qlvbars > i",
      models: ".ovmr",
      requests: ".qrq:not(.qrqh)",
      days: ".yrsv > g",
      bars: ".qch2 > svg > rect",
    },
    text: {
      noticeTitle: ".qnote > div > b",
      noticeBody: ".qnote > div > span",
      health: ".qlvl > div > b",
      healthDetail: ".qlvl > div > span",
      activityTotal: ".qlvs .num",
      spendWhole: ".qkpi:nth-child(1) > b",
      spendFraction: ".qkpi:nth-child(1) > b > .ct",
      spendChange: ".qkpi:nth-child(1) > em",
      requestTotal: ".qkpi:nth-child(2) > b",
      requestAverage: ".qkpi:nth-child(2) > em",
      latency: ".qkpi:nth-child(3) > b",
      successRate: ".qkpi:nth-child(3) > em",
      budget: ".qkpi:nth-child(4) > b",
      budgetDetail: ".qkpi:nth-child(4) > em",
    },
    attributes: { sparkline: { selector: ".qkpi .qsp polyline", attribute: "points" } },
  },
]);

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
  m2687ccdbb1: "Docs",
  m1e2df9c307: "Log in",
  mc4fb62202b: "Sign up",
  m3272fe53c0: "Take the tour",
  mb90a8ac9c4: "Choose a language",
  m3fd47edce4: "Close",
  m4144869c18: "Search languages",
  m9937e365aa: "简体中文",
  m854975fac5: "On this page",
  m6ebdfb798f: "Surface",
  mf3b3924597: "Connect",
  m446d270979: "Models and routes",
  mc7765bcffd: "Three steps",
  m97d3aaa40b: "Before production",
  m45a6d115fd: "FAQ",
  m8c0f8088c6: "Seabed",
  mcaf724c6bd: "Sign up and get an API key",
  m2e2a3b1e5d: "One key for every model",
  m0d0cee5072: "Every route is tested. You pay for the route that served you.",
  md006830a9e: "Read the docs",
};
const links = (anchors: string) =>
  Object.fromEntries(
    ["menuModelsHref", "menuPricingHref", "menuDocsHref", "menuSignInHref"].map((name, i) => [
      name,
      { selector: `${anchors}:nth-child(${i + 1})`, attribute: "href" },
    ]),
  );
const otherLanguages = ["zh-TW", "ja", "ko", "es", "de", "fr", "pt", "ru"].map((code) => `#lp-${code}`);
const siteRegions: Region[] = [
  ...["Site", "SitePhone"].map(
    (board): Region => ({
      board,
      name: "Shell",
      roots: [".tr.site"],
      // the lower screens' canvases, the sign-in page and the transition come with S2 and S3; the site has two languages
      omit: [".tr > .cw", ".tr > .wh", ".tr > .lg", ".tr > .fly", ".tr > canvas.ui", ...otherLanguages],
      children: ".hs",
      text: {
        models: ".bar .nav.mid a:nth-child(1) > span",
        pricing: ".bar .nav.mid a:nth-child(2) > span",
        docs: ".bar .nav.mid a:nth-child(3) > span",
        language: ".bar .lang [data-i18n]",
        signIn: ".bar .login > span",
        signUp: ".bar .btn.primary > span",
      },
      attributes: {
        home: { selector: ".bar .brand", attribute: "href" },
        modelsHref: { selector: ".bar .nav.mid a:nth-child(1)", attribute: "href" },
        pricingHref: { selector: ".bar .nav.mid a:nth-child(2)", attribute: "href" },
        docsHref: { selector: ".bar .nav.mid a:nth-child(3)", attribute: "href" },
        signInHref: { selector: ".bar .login", attribute: "href" },
        signUpHref: { selector: ".bar .btn.primary", attribute: "href" },
        // the same pages again in the island (wide) or in the menu sheet (narrow)
        ...links(board === "Site" ? ".isl .ixi a" : ".sheet .shl a"),
        menuSignUpHref: { selector: ".isl .btn", attribute: "href" },
        ...(board === "Site" ? {} : { sheetSignUpHref: { selector: ".sheet .shf a", attribute: "href" } }),
      },
      classes: {
        modeCls: ["dark", "light"],
        barCls: ["away"],
        islCls: ["on", "hide", "open"],
        tourCls: ["gone"],
        sheetCls: ["on"],
      },
      english: siteEnglish,
    }),
  ),
  {
    board: "Site",
    name: "FirstScreen",
    roots: [".hs > main"],
    children: "h1 > [data-i18n]",
    text: {
      lede: ".lede > span",
      signUp: ".cta .primary > span",
      docs: ".cta .outline > span",
    },
    attributes: {
      signUpHref: { selector: ".cta .primary", attribute: "href" },
      docsHref: { selector: ".cta .outline", attribute: "href" },
    },
    english: siteEnglish,
  },
  {
    board: "Site",
    name: "Headline",
    roots: ["h1 > [data-i18n] > .ph"],
  },
];

export const regions: Region[] = [...consoleRegions, ...siteRegions];
