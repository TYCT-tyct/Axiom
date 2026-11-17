export type Region = {
  board: string;
  name: string;
  roots: string[];
  page?: string;
  omit?: string[];
  children?: string;
  // Elements whose content the page supplies, by prop name, as children does for its own.
  slots?: Record<string, string>;
  repeats?: Record<string, string>;
  text?: Record<string, string>;
  attributes?: Record<string, { selector: string; attribute: string }>;
  // What the product needs on an element that the drawing leaves out, by selector (a root included): another tag, where
  // "link" renders the router link the page passes as `link`; attributes set, whose values may hold {{holes}}; and
  // unless, a boolean hole that leaves the element out while true.
  elements?: Record<string, { tag?: string; set?: Record<string, string>; unless?: string }>;
  classes?: Record<string, string[]>;
  english?: Record<string, string>;
};

// The console's pages that the shell links to, by the board's page key. The shell's departures from the drawing are
// holes the page fills: a region rendered with the drawing's own values is the drawing (boards.spec), and the console
// supplies the document scroll, its fonts, and leaves out what is not built (#816), as values.
const account = ["profile", "security", "notify"];
const pages = ["ov", "logs", "keys", "presets", "byok", "bill", ...account];
const unbuiltPages = ["usage", "route", "plugins", "budget", "observe", "mkeys", "prefs"];
const link = (selector: string, pg: string) => ({
  [selector]: { tag: "link", set: { href: `{{to_${pg}}}`, hidden: `{{hide_${pg}}}` } },
});
const links = (scope: string, keys: string[]) =>
  Object.assign({}, ...keys.map((pg) => link(`${scope}[data-pg=${pg}]`, pg)));
// A menu entry the board draws as a clickable <div> takes focus and the keys a button does, and keeps its look.
const button = { role: "button", tabindex: "0", onkeydown: "{{press}}" };
const expanded = (hole: string) => ({ "aria-expanded": `{{${hole}}}` });
const unbuilt = { unless: "unbuilt" };
const placeholder = { set: { style: "{{placeholder}}" } };
const styled = (hole: string, more: Record<string, string> = {}) => ({ set: { style: `{{${hole}}}`, ...more } });
const shellEnglish = {
  m5ce662ac8d: "Account settings",
  md041c02e58: "Search pages and keys, or type a command",
  m44ce7ae909: "Search",
  mdc569f3d8d: "Balance",
  m2c363db2ea: "Top up",
  ma57cfcb842: "Help",
  m83db2e5d84: "Models and pricing",
  mb94d13a45e: "Profile",
  mdfe0487d70: "Sign-in and security",
  m4b6ded8a7f: "Notifications",
  m86a63f23a0: "Appearance",
  m217cfe7db1: "System",
  maa0819dfc4: "Light",
  ma6b75d0680: "Dark",
  m3ab8cc1593: "Sign out",
  mfea405f9b0: "Overview",
  m7dbac1c20f: "Logs",
  m38844b135c: "More",
  mf1de1e2621: "Presets",
  m58aeb636ad: "Bring your own key",
  mc15323851b: "Billing",
  m572cf45ba4: "Back",
  m1bf1ab1035: "Collapse or expand the sidebar",
  m81e531c3b1: "Appearance and language",
  // What the drawing shows that the console hides or has not built (#816), translated so the region stays complete.
  md1f3e891d3: "At the 7-day daily average of ¥8.88, it runs out on ",
  m3bb8a15d6e: "November 17",
  m9e576b565a: ".",
  md5f5a7a010: "Today",
  m47912ebb84: "Nov",
  m57844391cf: "Dec",
  m0370bac54b: "Automatic top-up",
  m1a03b814c2: "Top up ¥200 when the balance falls below ¥50",
  me0b97e179f: "Report a problem",
  mc044f887b3: "Contact us",
  m2687ccdbb1: "Docs",
  me8a4f7c09d: "Service status",
  m42723e8b49: "Usage",
  mb9670c85a4: "Routing",
  me806fbbe8e: "Plugins",
  mbf13911816: "Guardrails and budgets",
  mba1902c7c9: "Observability",
  m650eb042f0: "Management keys",
  m15dec50795: "Personal",
  m1eaba8087e: "Production",
  m7ea2dbe244: "Evaluation",
};

// The shown frame: the bar, the page's area and, on the wide board, the sidebar, each with a style the page supplies.
const frame = (app: string) => ({
  ".app2": styled("appStyle", { "data-shell": app }),
  ".gbar": styled("barStyle"),
  "main.scr": styled("mainStyle", { id: "main", tabindex: "-1" }),
  ".qpages": styled("pagesStyle"),
  // Of the help menu only the models page is built.
  ".qhlp > :not([onclick*=soonM])": unbuilt,
  [unbuiltPages.map((pg) => `:is(.qnav, .otabs a, .qmi)[data-pg=${pg}]`).join(", ")]: unbuilt,
});

// The bar's menus, the same on both boards.
const menus = {
  ".glogo": { tag: "link", set: { href: "{{to_ov}}", "aria-label": "{{home}}" } },
  ".ghlp": { set: expanded("helpOpen") },
  ".qhlp > [onclick*=soonM]": { tag: "link", set: { href: "{{to_models}}" } },
  ".qgav": { set: expanded("meOpen") },
  ".qme [data-pg=prefs]": { set: { ...button, lang: "{{languageTag}}" } },
  ".qme .pmi.mut2": { set: button },
  ".tseg > button:nth-of-type(1)": { set: { "aria-pressed": "{{themeSystem}}" } },
  ".tseg > button:nth-of-type(2)": { set: { "aria-pressed": "{{themeLight}}" } },
  ".tseg > button:nth-of-type(3)": { set: { "aria-pressed": "{{themeDark}}" } },
  ...links(".qme .pmi", account),
};

const wide = {
  ...frame("app"),
  ".pm": { set: { "data-shell": "popover" } },
  ".qbc": { set: { "aria-label": "{{path}}" } },
  ".qnv": { set: { "aria-label": "{{navigation}}" } },
  // The space switch (O1: no team spaces) and what the balance popover estimates and tops up automatically.
  ".gbar .qbc > .pw:first-child, .bp .bpt, .bp .rwb, .bp .rwl, .bp .pmd, .bp .bpr": unbuilt,
  // The command palette is not built; its place in the bar's grid stays.
  ".gsr": placeholder,
  ".gsr .mono": styled("monoStyle"),
  ".abody": styled("bodyStyle", { "data-shell": "body" }),
  ".sbar": styled("sidebarStyle"),
  ".mainw2": styled("panelStyle", { "data-shell": "panel" }),
  ".g11r > .pw:first-child": { set: { hidden: "{{hide_bill}}" } },
  ".gbal": { set: expanded("balanceOpen") },
  ".scol": { set: expanded("sidebarOpen") },
  ...links(".qnav", pages),
};

const phone = {
  ...frame("phone"),
  ".pm": styled("sheetStyle", { "data-shell": "popover" }),
  // The space switch and the command palette; the bar keeps their places, as it lays out the buttons after them.
  ".gbar > .qbcp, .gsi": placeholder,
  ".otabs": styled("tabsStyle", { "aria-label": "{{navigation}}", "data-shell": "tabs" }),
  ".otabs > a:not([data-pg])": { set: { ...button, ...expanded("moreOpen") } },
  ...links(".otabs > a", ["ov", "logs", "keys"]),
  ...links(".qmi", ["presets", "byok", "bill"]),
};

const shell = (board: string): Region => ({
  board,
  name: "Shell",
  roots: [".board"],
  // Besides the drawing's own omissions, what it hides in the personal space it shows: the team and workspace levels
  // and the space switch's menus (O1, #816). Left out, they cost the console nothing; the drawing is unchanged.
  omit: [
    ".qdrs",
    ".qdr",
    ".kov",
    ".tst",
    ".pnS",
    ".pnW",
    ".qspm",
    ".gbar .qbc > .onW",
    ".gbal .sxT",
    ".bp > .sxT",
    ".qswp",
    ".qmore > .onW",
    ".qmore > .onS2",
    ".otabs.onS2",
  ],
  children: ".qpages",
  // The site's logo, or its initial where the board draws an A.
  slots: { mark: ".glogo .bm" },
  english: shellEnglish,
  text: {
    // The language entry names the language it switches to.
    language: ".qme [data-pg=prefs] > span",
    ...(board === "AxApp"
      ? {
          balance: ".gbal .sxP",
          balanceCurrency: ".bp > .sxP .bpv .cy",
          balanceWhole: ".bp > .sxP .bpv",
          balanceFraction: ".bp > .sxP .bpv .ct",
        }
      : {}),
  },
  // The drawing's own frame size and language; the board's style keeps its {{vars}}, after which the page's declarations
  // win.
  attributes: {
    lang: { selector: ".board", attribute: "lang" },
    frameStyle: { selector: ".frame", attribute: "style" },
  },
  elements: { ...menus, ...(board === "AxApp" ? wide : phone) },
});

// The labels the Overview slice's demo account came with, in English: regions.ts is the only place a translation is
// written, and the generated .en.ts takes them from here (README「画板机械转换」).
const overviewEnglish = {
  mba00f96381: "Retried on another route",
  m053461ce86: "Success",
  mfea405f9b0: "Overview",
  m569c43d6cc: "Copy Base URL",
  mc7b62164df: "New key",
  m02e9734a51: "Spent this month",
  m393e124155: "Requests",
  mfd198d22ce: "Median first token",
  mbd8dbd5724: "Budget",
  ma3cc9c5725: "Daily spend",
  m19e7c0223e: "By model",
  m5073e9f5ed: "See usage",
  m5de8778457: "Recent requests",
  m27975a68f2: "All logs",
  m8b6ff49851: "Time",
  mc98e118e0a: "Model",
  m6320b4a872: "Status",
  m7fe8603c08: "Cost",
  mfa10f1a884: "+23% vs the same days of September",
  m3680acf4d7: "¥0.062 per request on average",
  m7b1ed4bf77: "99.76% succeeded",
  m1ab89346ec: "¥186 of ¥300, ¥248 expected by month end",
};

// The console has only the calls the API can answer: the live state bar, the year heatmap, the month-end forecasts and
// the team workspaces' views stay out (#816, O23). The chart's currency axis goes because the board writes ¥ in its
// CSS; the page's money comes from the account's currency.
function overviewRegion(board: string): Region {
  return {
    board,
    name: "Overview",
    roots: [".p-ov"],
    page: "ov",
    omit: [
      ".qlivec",
      ".qyr",
      ".qleg",
      ".fut",
      ".qtip",
      ".qhits",
      ".qyax",
      ".qgl",
      ".qkpi .qsp",
      ".qkpi:nth-child(1) .cy",
      ".qkpi:nth-child(3) .cy",
      ".qkpi:nth-child(4) .cy",
      ".wv.w0",
      ".wv.w1",
    ],
    repeats: {
      models: ".ovmr",
      requests: ".qrq:not(.qrqh)",
      bars: ".qch2 > svg > rect",
    },
    text: {
      spendWhole: ".qkpi:nth-child(1) > b",
      spendFraction: ".qkpi:nth-child(1) > b > .ct",
      spendNote: ".qkpi:nth-child(1) > em",
      requestTotal: ".qkpi:nth-child(2) > b",
      requestAverage: ".qkpi:nth-child(2) > em",
      balance: ".qkpi:nth-child(3) > b",
      balanceNote: ".qkpi:nth-child(3) > em",
      keyCount: ".qkpi:nth-child(4) > b",
      keyNote: ".qkpi:nth-child(4) > em",
      chartDate: ".qbl span",
    },
    attributes: { chartDateLeft: { selector: ".qbl span", attribute: "style" } },
    english: overviewEnglish,
  };
}

// Only the page card adds regions. Selectors refer to the pinned design, never to generated JSX.
const consoleRegions: Region[] = ["AxApp", "AxAppPhone"].flatMap((board): Region[] => [
  shell(board),
  overviewRegion(board),
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

export const regions: Region[] = [...consoleRegions, ...siteRegions];
