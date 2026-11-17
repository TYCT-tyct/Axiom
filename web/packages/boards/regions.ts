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
  // "link" renders the router link the page passes as `link`, and attributes set, whose values may hold {{holes}}.
  elements?: Record<string, { tag?: string; set?: Record<string, string> }>;
  classes?: Record<string, string[]>;
  english?: Record<string, string>;
};

// The console's pages that the shell links to, by the board's page key. The shell's departures from the drawing are
// holes the page fills: a region rendered with the drawing's own values is the drawing (boards.spec), and the console
// supplies the document scroll, its fonts, and "display: none" for what is not built (#816) as values.
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
const unbuilt = { set: { style: "{{unbuilt}}" } };
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
  m0d8f3231cc: "Lin Zhiyuan",
  m364ca15da1: "Team, 6 members",
  me68cd1fada: "New team",
  m8714d7f5e9: "Axiom Labs workspaces",
  m5fa0b0f66f: "¥842.17 this month",
  maafe08594f: "¥118.40 this month",
  m946897d107: "New workspace",
  md1f3e891d3: "At the 7-day daily average of ¥8.88, it runs out on ",
  m3bb8a15d6e: "November 17",
  m9e576b565a: ".",
  md5f5a7a010: "Today",
  m47912ebb84: "Nov",
  m57844391cf: "Dec",
  m0370bac54b: "Automatic top-up",
  m1a03b814c2: "Top up ¥200 when the balance falls below ¥50",
  md711568daf: "At the 7-day daily average of ¥47.04, it runs out on ",
  mf0b3ec59c3: "November 20",
  ma0975bc7b9: "Top up ¥1,000 when the balance falls below ¥300",
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
  m3797982942: "Workspaces",
  m6e6d6ddbb7: "Members",
  ma5c97a7a7e: "Security and compliance",
  md44e0d633b: "Team settings",
  mdf3d58c7d8: "Settings",
  m4bf3c13e58: "Billing",
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
  omit: [".qdrs", ".qdr", ".kov", ".tst"],
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
