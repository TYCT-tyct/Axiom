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

// Each console page compares the frame around its page, so it carries the frame's rulings too.
const pageScenes = ["console overview", "console keys", "console route", "console byok"];
const shell: Ruling[] = [
  { basis: "the page in the frame is compared in its own scene", ...both(".qpages") },
  ...frame,
].map((ruling) => inScene("console shell", ruling));

// C7 (L1, L23) composes existing board regions; AxApp contains no models, announcements or legal screen to compare.
// #816 G4 defers endpoint speed, quality score and source, so these fields appear in neither model list nor detail.
export const rulings: Ruling[] = [
  ...shell,
  ...pageScenes.flatMap((scene) => frame.map((ruling) => inScene(scene, ruling))),
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
  ...frame.map((ruling) => inScene("console bill", ruling)),
  {
    scene: "console bill",
    basis: "#816 O23: run-out forecast has no basis on the backend",
    board: { selector: ".p-bill > .sxP > .qcard.hero .qhl > p, .p-bill > .sxP > .qcard.hero .qrun" },
  },
  {
    scene: "console bill",
    basis: "#816 O19, O20: auto top-up and invoices & bank transfer are not built",
    board: { selector: ".p-bill > .sxP > .qcard.rows" },
  },
  {
    scene: "console bill",
    basis: "L15: the account balance and batch rows replace the board's demo figures and statement rows",
    ...both(".p-bill .qcard.hero .qhl > b.qbig.xl, .p-bill .qcard.flush .qtbl tbody td"),
  },
  {
    scene: "console bill",
    basis: "L15: billing navigation and the balance breakdown are live API sections absent from the board",
    page: { selector: ".p-bill > .qph > .qpa nav, .p-bill > div:last-child section" },
  },
  // Axiom CJK Console is cut at a single static weight (wght 330, web/scripts/fonts/sources.json), whereas the board
  // draws MiSansVF at 500 (h1) (docs/frontend.md「字体」).
  {
    scene: "console bill",
    basis: "§5.4: Axiom CJK Console is cut at wght 330 (sources.json), while the board draws MiSansVF at 500",
    ...both(".p-bill .qph h1"),
  },
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
    basis: "the page's own name, description and its list's title and hint say what the console lists",
    board: { selector: ".qph .qptl h1, .qph p, .qcard.flush .qch" },
    page: { selector: ".qph .qptl h1, .qph p, .qcard.flush .qch" },
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
  // The page's name and description are the console's words, and its action area the console's own: the design gives the
  // BYOK page neither (console-gap L20). The whole header goes, so the two shots' differing text widths leave no edge.
  { scene: "console byok", basis: "console-gap L20: the page's own name, description and add action", ...both(".qph") },
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
