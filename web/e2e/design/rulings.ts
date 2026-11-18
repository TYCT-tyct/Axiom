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
const shell: Ruling[] = [
  { basis: "the page in the frame is compared in its own scene", ...both(".qpages") },
  { basis: "O1: no team spaces", board: { selector: spaceSwitch } },
  { basis: "#816: the workspace level is not built", board: { selector: ".gbar .qbc > .onW, .gbar > .qbcp" } },
  { basis: "the account and the balance are the API's, not the board's demo", ...both(".gbal, .qgav") },
  {
    basis: "#816: usage, routing, plugins, budgets, observability and management keys are not built",
    ...both(".pnP .qnv, .otabs"),
  },
  { basis: "#816: the command palette is not built", ...both(".gsr, .gsi") },
].map((ruling) => ({ scene: "console shell", ...ruling }));

export const rulings: Ruling[] = [
  ...shell,
  // The console has no teams: the board's header switches between the personal space and a team (the phone board's
  // is a single button); the page has none.
  { scene: "console overview", basis: "O1: no team spaces", board: { selector: spaceSwitch } },
  { scene: "console keys", basis: "O1: no team spaces", board: { selector: spaceSwitch } },
  { scene: "console route", basis: "O1: no team spaces", board: { selector: spaceSwitch } },
  { scene: "console byok", basis: "O1: no team spaces", board: { selector: spaceSwitch } },
  // The board draws a workspace's supplier lines — latency, availability, a relative price and a switch per line — and
  // the failover switch and timeouts above them. No endpoint names a line, and a failed attempt is the platform's
  // (#816 O13); what a key routes by is a preset, which the design has no editor for (console-gap L2). So the page
  // lists presets instead, and what the board draws there is excused.
  {
    scene: "console route",
    basis: "#816 O13: the failover switch, the timeouts and the sort a workspace prefers have no account-level setting",
    board: { selector: ".qstrc, .qcard.rows" },
  },
  {
    scene: "console route",
    basis:
      "#816: the board's line table has no API; the page lists routing presets, whose columns and cells are the account's",
    board: { selector: ".qtbl" },
    page: { selector: ".qtbl" },
  },
  // The page's own words where the board writes about lines it draws, and the console's create action, which the design
  // puts on no page (console-gap L2).
  {
    scene: "console route",
    basis: "the page's own description and its list's title and hint say what the console lists",
    board: { selector: ".qph p, .qcard.flush .qch" },
    page: { selector: ".qph p, .qcard.flush .qch" },
  },
  {
    scene: "console route",
    basis: "console-gap L2: the design draws nowhere to make a preset, so the page's action area is its own",
    page: { selector: ".qph .qpa" },
  },
  // The board draws four vendors, a workspace-wide "prefer your own key" switch and no test, switch or removal per key.
  // The API takes one provider (#816 O14) and its fallback is each key's own, and the test, switch and removal are in the
  // row's dialog (console-gap L20), so the list and those rows are excused.
  {
    scene: "console byok",
    basis: "#816 O14/L20: one provider, a fallback per key, and the per-key actions in the row's own dialog",
    board: { selector: ".p-byok .qcard.rows" },
    page: { selector: ".p-byok .qcard.rows" },
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
