// The differences between a board and its page that a product ruling makes on purpose, by design scene
// (design-map.ts). Each names the region on each side, a CSS selector or a rectangle in CSS pixels of the viewport, and
// its basis: the ruling's number (R#, L#, O#, G# of the port design, see #747) and what it rules. A side without the
// region leaves it out. Rectangles from both sides are blanked on both shots; canvas regions replace only the bitmap
// on their own side. Nothing else is excused beyond anti-aliasing (compare.ts). A selector that matches nothing fails
// the comparison, so a ruling cannot outlive the region it was written for.
// A canvas region replaces only its bitmap, before compositing. Text and translucent layers above it remain tested.
export type Region =
  | { selector: string }
  | { canvas: string }
  | { x: number; y: number; width: number; height: number };

export type Ruling = { scene: string; basis: string; board?: Region; page?: Region };

const spaceSwitch = ".gbar .qbc > .pw:first-child, .gbar > .qbcp";

// The shell's rulings, at every width, so each selector names the wide board's part and the phone's: what the page
// inside the frame shows, the board's demo account and balance, and what is not built (#816).
const both = (selector: string) => ({ board: { selector }, page: { selector } });
// The frame the console's pages sit in, as the shell's own scene excuses it: the board's demo account and balance, the
// workspace level, and the entries of pages that are not built (#816). A page's scene compares the page, so it excuses
// the same frame the same way.
const frame: Omit<Ruling, "scene">[] = [
  { basis: "O1: no team spaces", board: { selector: spaceSwitch } },
  { basis: "#816: the workspace level is not built", board: { selector: ".gbar .qbc > .onW, .gbar > .qbcp" } },
  { basis: "the account and the balance are the API's, not the board's demo", ...both(".gbal, .qgav") },
  {
    basis: "#816: usage, routing, plugins, budgets, observability and management keys are not built",
    ...both(".pnP .qnv, .otabs"),
  },
  { basis: "#816: the command palette is not built", ...both(".gsr, .gsi") },
  // The mark is the site's, from its own brand, not the board's demo logo.
  { basis: "the brand mark is the site's, not the board's", ...both(".glogo .bm") },
];
const inScene = (scene: string, region: Omit<Ruling, "scene">): Ruling => ({ scene, ...region });

const shell: Ruling[] = [
  { basis: "the page in the frame is compared in its own scene", ...both(".qpages") },
  ...frame,
].map((ruling) => inScene("console shell", ruling));

const accountShell = (scene: string): Ruling[] =>
  [
    { basis: "O1: no team spaces", board: { selector: spaceSwitch } },
    { basis: "#816: the workspace level is not built", board: { selector: ".gbar .qbc > .onW, .gbar > .qbcp" } },
    { basis: "the account and the balance are the API's, not the board's demo", ...both(".gbal, .qgav") },
    {
      basis: "#816: usage, routing, plugins, budgets, observability and management keys are not built",
      ...both(".pnP .qnv, .otabs"),
    },
    { basis: "#816: the command palette is not built", ...both(".gsr, .gsi") },
    {
      basis: "§5.4: Chinese at 500 is drawn at 400 until the coverage crawl",
      ...both(".gbar .qbct, .qph h1"),
    },
  ].map((ruling) => ({ scene, ...ruling }));

// C7 (L1, L23) composes existing board regions; AxApp contains no models, announcements or legal screen to compare.
// #816 G4 defers endpoint speed, quality score and source, so these fields appear in neither model list nor detail.
export const rulings: Ruling[] = [
  ...shell,
  ...accountShell("console settings"),
  {
    scene: "console settings",
    basis: "#816: account deletion has no API or owner refund rule yet",
    board: { selector: ".p-profile .qcard.danger" },
  },
  {
    scene: "console settings",
    basis:
      "§5.4 & API account data: avatar glyph, weight-500 labels, hints and inputs differ; cards below Profile live outside the Profile artboard",
    board: { selector: ".p-profile .qph p, .p-profile .qav.lg, .p-profile .qrow .qrl, .p-profile .qin" },
    page: {
      selector:
        ".p-profile .qph p, .p-profile .qav.lg, .p-profile .qrow .qrl, .p-profile .qin, .p-profile ~ .qpg.p-security",
    },
  },
  ...accountShell("console security"),
  {
    scene: "console security",
    basis:
      "#816 & §5.4: team TOTP policy lock, API row text, 500/330 CJK glyph weight inside spans, and cards below Sessions differ from board demo",
    board: {
      selector:
        ".p-security .qph p, .p-security .qcard:first-of-type > .qrow:first-child > .qrl, .p-security .qcard:first-of-type > .qrow:nth-child(2), .p-security .qcard:nth-of-type(2) .qses > div, .p-security .qch h2, .p-security .btn > span, .p-security .qtag",
    },
    page: {
      selector:
        ".p-security .qph p, .p-security .qcard:first-of-type > .qrow:first-child > .qrl, .p-security .qcard:first-of-type > .qrow:nth-child(2), .p-security .qcard:nth-of-type(2) .qses > div, .p-security .qch h2, .p-security .btn > span, .p-security .qtag, .p-security ~ .qpg.p-security, .p-profile",
    },
  },
  ...accountShell("console notifications"),
  {
    scene: "console notifications",
    basis:
      "#816 G9: in-app toggle column, unbuilt rows 5-6, row 3 state, unified '接收提醒' header and delivery/language cards below Alerts differ from board demo",
    board: {
      selector:
        ".p-notify .qph p, .p-notify .qnt.hd > span:not(:first-child), .p-notify .qnt:not(.hd) > div, .p-notify .qnt:not(.hd) > button:nth-of-type(1), .p-notify .qnt:nth-child(4) > button:nth-of-type(2), .p-notify .qcard > .qnt:nth-of-type(n+6)",
    },
    page: {
      selector:
        ".p-notify .qph p, .p-notify .qnt.hd > span:not(:first-child), .p-notify .qnt:not(.hd) > div, .p-notify .qnt:nth-child(4) > button:nth-of-type(1), .p-profile, .p-security",
    },
  },
  // The console has no teams: the board's header switches between the personal space and a team (the phone board's
  // is a single button); the page has none.
  { scene: "console overview", basis: "O1: no team spaces", board: { selector: spaceSwitch } },
  ...frame.map((ruling) => inScene("console keys", ruling)),
  // The board's key list has nothing to narrow it by (L8), so the page keeps the filters it had behind a button beside
  // the actions, in buttons of the console's own.
  {
    scene: "console keys",
    basis: "L8: the board's list has no filters; the page's controls sit in the header's action area",
    board: { selector: ".p-keys .qph > .qpa" },
    page: { selector: ".p-keys .qph > .qpa" },
  },
  // The board calls the used column the month's spending; the API says what a key used of its limit's own window, which
  // the page names in the header and states per row.
  {
    scene: "console keys",
    basis: "G3: the used column is the limit's own period, not the calendar month the board names",
    board: { selector: ".p-keys .qkeyt thead th:nth-child(2)" },
    page: { selector: ".p-keys .qkeyt thead th:nth-child(2)" },
  },
  // The rows are the account the scene signs in as, not the board's demo account: three keys against five, and another
  // currency, limits, names and times, so the card that holds them differs.
  {
    scene: "console keys",
    basis: "G3: the rows are the scene's account keys, not the board's demo keys",
    board: { selector: ".p-keys .wv.w2 .qcard" },
    page: { selector: ".p-keys .wv.w2 .qcard" },
  },
  // #816 lists what the API cannot answer yet: the overview leaves those panels out, and the board's own account data
  // is not what the page shows, so the board's rendering of them is excused.
  {
    scene: "console overview",
    basis: "#816 O23: the live state bar needs hourly counts and attempts the API does not aggregate",
    board: { selector: ".qlivec" },
  },
  {
    scene: "console overview",
    basis: "#816 O23: the year heatmap needs more than 31 days, which the API does not aggregate",
    board: { selector: ".qyr" },
  },
  {
    scene: "console overview",
    basis: "#816 O23: month-end and run-out forecasts have no basis",
    board: { selector: ".qleg, .qch2 .fut" },
  },
  {
    scene: "console overview",
    basis: "#816 G1: the median first token needs an aggregation the API does not offer; the page shows the balance",
    board: { selector: ".qkpis .qkpi:nth-child(3)" },
    page: { selector: ".qkpis .qkpi:nth-child(3)" },
  },
  {
    scene: "console overview",
    basis: "#816 G1: the budget card and its forecast have no endpoint here; the page shows the account's keys",
    board: { selector: ".qkpis .qkpi:nth-child(4)" },
    page: { selector: ".qkpis .qkpi:nth-child(4)" },
  },
  {
    scene: "console overview",
    basis: "G1: the board writes ¥ in its spend axis; the page's money is in the account's currency",
    board: { selector: ".qyax, .qgl" },
  },
  {
    scene: "console overview",
    basis: "L23: the console shows site announcements, which the overview board does not draw",
    page: { selector: ".qcard:has(> details[data-announcements])" },
  },
  {
    scene: "converted-Login",
    basis: "animated engine; covered by its fixed-seed engine test",
    board: { canvas: "canvas.sea" },
    page: { canvas: "canvas.sea" },
  },
  {
    scene: "converted-ClawdEgg",
    basis: "animated engine; covered by its fixed-seed engine test",
    board: { canvas: "canvas.sea" },
    page: { canvas: "canvas.sea" },
  },
  {
    scene: "converted-ClawdEgg",
    basis: "animated engine; covered by its fixed-seed engine test",
    board: { canvas: "canvas.cw" },
    page: { canvas: "canvas.cw" },
  },
  // The board runs no engines for product comparisons (board.ts): it draws no sea, and without prefs.js its theme
  // button keeps the light page's moon in both schemes. The page's sea and switches are fx.spec.ts's.
  {
    scene: "site home",
    basis: "§8.2: engines are off on the board; the sea is tested by fx.spec.ts",
    page: { canvas: "canvas.sea" },
  },
  {
    scene: "site home",
    basis: "§8.2: engines are off on the board; the theme switch is tested by fx.spec.ts",
    board: { selector: ".bar .theme" },
    page: { selector: ".bar .theme" },
  },
  // The site has no Chinese cut at 500 until the coverage crawl (port design §5.4) says which characters need one: the
  // buttons' Chinese is drawn at 400 (docs/frontend.md「字体」).
  {
    scene: "site home",
    basis: "§5.4: Chinese at 500 is drawn at 400 until the coverage crawl",
    board: { selector: ".ho .btn" },
    page: { selector: ".ho .btn" },
  },
  // Links and controls with nowhere to go yet are left out until their pages come: the docs page (S4) and the screens
  // below the first, which the tour goes through (S2). The rows they sat in close up.
  {
    scene: "site home",
    basis: "S4: no docs page yet",
    board: { selector: ".bar .nav.mid, .hs > .main .cta" },
    page: { selector: ".bar .nav.mid, .hs > main .cta" },
  },
  { scene: "site home", basis: "S2: no screens below the first yet", board: { selector: ".tour" } },
];
