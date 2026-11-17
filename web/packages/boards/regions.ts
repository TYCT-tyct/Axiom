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
  // What the product needs on an element that the drawing leaves out, by selector (a root included): another tag, where
  // "link" renders the router link the page passes as `link`, and attributes set, whose values may hold {{holes}}.
  elements?: Record<string, { tag?: string; set?: Record<string, string> }>;
  classes?: Record<string, string[]>;
  english?: Record<string, string>;
};

// The console's pages that the shell links to, by the board's page key; the others the board draws are not built yet
// (#816) and are left out.
const account = ["profile", "security", "notify"];
const pages = ["ov", "logs", "keys", "presets", "byok", "bill", ...account];
const unbuilt = ["usage", "route", "plugins", "budget", "observe", "mkeys", "prefs"].map((pg) => `[data-pg=${pg}]`);
const link = (selector: string, pg: string) => ({
  [selector]: { tag: "link", set: { href: `{{to_${pg}}}`, hidden: `{{hide_${pg}}}` } },
});
const links = (scope: string, keys: string[]) =>
  Object.assign({}, ...keys.map((pg) => link(`${scope}[data-pg=${pg}]`, pg)));
// A menu entry the board draws as a clickable <div> is a button, so the keyboard reaches it.
const button = { tag: "button", set: { type: "button" } };
const expanded = (hole: string) => ({ "aria-expanded": `{{${hole}}}` });
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
  // The phone's hidden space switch.
  m15dec50795: "Personal",
  m1eaba8087e: "Production",
  m7ea2dbe244: "Evaluation",
};

const shellOmit = [
  ".pscr",
  ".qdrs",
  ".qdr",
  ".kov",
  ".tst",
  // Team spaces and the workspace level are not built (#816); the personal space is the console's only one.
  ".gbar .qbc > .pw:first-child",
  ".gbar .qbc > .onW",
  ".pnS",
  ".pnW",
  ".qswp",
  ".qmore > .onW",
  ".qmore > .onS2",
  ".otabs.onS2",
  ".bp > .sxT",
  ".gbal .sxT",
  // What the balance popover estimates and the automatic top-up are not built (#816).
  ".bp .bpt",
  ".bp .rwb",
  ".bp .rwl",
  ".bp .pmd",
  ".bp .bpr",
  // Of the help menu only the models page is built.
  ".qhlp > :not([onclick*=soonM])",
  ...unbuilt.map((pg) => `:is(.qnav, .otabs a, .qmi)${pg}`),
];

// The document scrolls, not the board's fixed frame (port design §3.3): the bar sticks to the top, the sidebar below
// it, the phone's tabs to the bottom, and the phone's popovers are sheets at the bottom of the window. Set inline, the
// frame's geometry outranks the board's rules, four classes deep. tokens.css carries the colours.
const frame = (app: string) => ({
  ".board": { set: { style: "", lang: "{{lang}}" } },
  ".frame": { set: { style: "overflow: clip; min-height: 100dvh" } },
  ".app2": { set: { style: "position: relative; inset: auto; min-height: 100dvh", "data-shell": app } },
  ".gbar": { set: { style: "position: sticky; top: 0; background: var(--page)" } },
  "main.scr": { set: { id: "main", tabindex: "-1", style: "overflow: visible; flex: 1 0 auto" } },
});

// The bar's menus, the same on both boards.
const menus = {
  ".glogo": { tag: "link", set: { href: "{{to_ov}}", "aria-label": "{{home}}" } },
  ".ghlp": { set: expanded("helpOpen") },
  ".qhlp > .pmi": { tag: "link", set: { href: "{{to_models}}" } },
  ".qgav": { set: expanded("meOpen") },
  ".qme [data-pg=prefs]": { ...button, set: { ...button.set, lang: "{{languageTag}}" } },
  ".qme .pmi.mut2": button,
  ".tseg > :nth-child(1)": { set: { "aria-pressed": "{{themeSystem}}" } },
  ".tseg > :nth-child(2)": { set: { "aria-pressed": "{{themeLight}}" } },
  ".tseg > :nth-child(3)": { set: { "aria-pressed": "{{themeDark}}" } },
  ...links(".qme .pmi", account),
};

const wide = {
  ...frame("app"),
  ".pm": { set: { "data-shell": "popover" } },
  ".qbc": { set: { "aria-label": "{{path}}" } },
  ".qnv": { set: { "aria-label": "{{navigation}}" } },
  // The command palette is not built (#816); its place in the bar's grid stays.
  ".gsr": { set: { style: "visibility: hidden" } },
  ".abody": { set: { "data-shell": "body", style: "flex: 1 0 auto" } },
  // 56px is the board's bar.
  ".sbar": { set: { style: "position: sticky; top: 56px; height: calc(100dvh - 56px)" } },
  ".mainw2": { set: { "data-shell": "panel", style: "overflow: clip" } },
  ".g11r > .pw:first-child": { set: { hidden: "{{hide_bill}}" } },
  ".gbal": { set: expanded("balanceOpen") },
  ".scol": { set: expanded("sidebarOpen") },
  ...links(".qnav", pages),
};

const phone = {
  ...frame("phone"),
  ".pm": { set: { "data-shell": "popover", style: "position: fixed" } },
  // The space switch is not built (#816); the bar keeps its place, as it lays out the buttons after it.
  ".gbar > .qbcp": { set: { style: "visibility: hidden" } },
  ".gsi": { set: { style: "visibility: hidden" } },
  // The board's fifth tab is not built (#816).
  ".otabs": {
    set: {
      "aria-label": "{{navigation}}",
      "data-shell": "tabs",
      style: "position: sticky; bottom: 0; grid-template-columns: repeat(4, 1fr)",
    },
  },
  ".otabs > a:not([data-pg])": {
    set: { role: "button", tabindex: "0", onkeydown: "{{moreKey}}", ...expanded("moreOpen") },
  },
  ...links(".otabs > a", ["ov", "logs", "keys"]),
  ...links(".qmi", ["presets", "byok", "bill"]),
};

const shell = (board: string): Region => ({
  board,
  name: "Shell",
  roots: [".board"],
  omit: shellOmit,
  children: ".qpages",
  english: shellEnglish,
  text: {
    // The language entry names the language it switches to.
    language: ".qme [data-pg=prefs] > span",
    ...(board === "AxApp"
      ? { balance: ".gbal .sxP", balanceCurrency: ".bpv .cy", balanceWhole: ".bpv", balanceFraction: ".bpv .ct" }
      : {}),
  },
  elements: { ...menus, ...(board === "AxApp" ? wide : phone) },
});

// Only the page card adds regions. Selectors refer to the pinned design, never to generated JSX.
export const regions: Region[] = ["AxApp", "AxAppPhone"].flatMap((board): Region[] => [
  shell(board),
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
