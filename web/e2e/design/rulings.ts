// The differences between a board and its page that a product ruling makes on purpose, by design scene
// (design-map.ts). Each names the region on each side, a CSS selector or a rectangle in CSS pixels of the viewport, and
// its basis: the ruling's number (R#, L#, O#, G# of the port design, see #747) and what it rules. A side without the
// region leaves it out. Rectangles from both sides are blanked on both shots; canvas regions replace only the bitmap
// on their own side. Nothing else is excused beyond anti-aliasing (compare.ts). A selector that matches nothing fails
// the comparison, so a ruling cannot outlive the region it was written for.
// A canvas region replaces only its bitmap, before compositing. Text and translucent layers above it remain tested.
type Region = { selector: string } | { canvas: string } | { x: number; y: number; width: number; height: number };

// glyphs: the ruling excuses only what is drawn inside its boxes, not where they are. Each side's boxes are blanked on
// both shots, so a box that moved would be blanked at both places and pass; with glyphs the page's boxes must also sit
// where the board's do, one for one.
export type Ruling = { scene: string; basis: string; board?: Region; page?: Region; glyphs?: true };

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
    basis: "#816: plugins, budgets, observability and management keys are not built",
    ...both(".pnP .qnv, .otabs"),
  },
  { basis: "#816: the command palette is not built", ...both(".gsr, .gsi") },
  // The mark is the site's, from its own brand, not the board's demo logo.
  { basis: "the brand mark is the site's, not the board's", ...both(".glogo .bm") },
];
const inScene = (scene: string, region: Omit<Ruling, "scene">): Ruling => ({ scene, ...region });

// Each console page compares the frame around its page, so it carries the frame's rulings too.
const presetScenes = ["console request-presets", "console request-presets en"];
const pageScenes = [
  "console overview",
  "console keys",
  "console usage",
  "console route",
  "console byok",
  "console calls",
  ...presetScenes,
];
const shell: Ruling[] = [
  { basis: "the page in the frame is compared in its own scene", ...both(".qpages") },
  ...frame,
].map((ruling) => inScene("console shell", ruling));

// The frame the home page's screens below the first are seen in: the page's header has given way to the island, which the
// board, its engines off, does not do, the sea is the page's own, and the buttons' Chinese is drawn at 400 (§5.4).
const screenFrame: Omit<Ruling, "scene">[] = [
  {
    basis: "the header gives way to the island on the page once scrolled; the board's engines are off",
    ...both(".bar, .isl"),
  },
  { basis: "§8.2: engines are off on the board; the sea is tested by fx.spec.ts", page: { canvas: "canvas.sea" } },
  {
    basis: "the tour button is the board's script's, which the harness leaves out; fx.spec.ts tests the tour",
    ...both(".tour"),
  },
  { basis: "§5.4: Chinese at 500 is drawn at 400 until the coverage crawl", ...both(".ho .btn") },
];
const siteScreens = ["site connect", "site faq", "site bed"];
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
    {
      basis:
        "appearance and language are a card of the settings page (frontend.md), not a fourth entry of the folded rail",
      board: { selector: "[data-pg=prefs]" },
    },
  ].map((ruling) => ({ scene, ...ruling }));

// The console's sign-in page against the Login board's email step (#747 S3, port design §9), with the page's words in
// the reference (design-map.ts). The sea is animated on both sides, and the stage that forms the step's shape on it is
// the transition's on the board (trans.js), so both canvases are the fx tests'.
// The reset page's request for a link and open registration are the same parts, and are excused the same.
const loginScenes = ["console sign-in", "console reset-password", "console register"];
const signIn = (ruling: Omit<Ruling, "scene">): Ruling[] => loginScenes.map((scene) => ({ scene, ...ruling }));
const signInRulings: Ruling[] = [
  // The sign-in pages draw their Chinese in the faces the board names, cut from their messages and the board's region
  // text: the title and the label match the board's. Only glyphs are excused, each where its text is drawn, so a line or
  // a control that moves, or a button or link that loses its fill or border, still fails.
  signIn({
    basis: "§5.4: Chinese at 500 is drawn at 400 until the coverage crawl",
    ...both(".lg-btn > span, .lg-top a"),
    glyphs: true,
  }),
  signIn({
    basis: "§5: a few characters of the cut body face differ from the board's source face (not yet explained)",
    ...both(".lg-sub > span, .lg-note > span, .lg-note > a, .lg-pitch .s > span, .lg-label"),
    glyphs: true,
  }),
  signIn({ basis: "§9 not ported: the footer's contact link (O21)", board: { selector: ".lg-foot" } }),
  signIn({
    basis: "§8.2: the sea is an animated engine on both sides; it is tested by fx.spec.ts",
    board: { canvas: "canvas.sea" },
    page: { canvas: "canvas.sea" },
  }),
  signIn({
    basis: "§9: the stage on the sea is the transition's on the board (trans.js); it is tested by fx.spec.ts",
    page: { canvas: ".lg-sea > canvas:not(.sea)" },
  }),
].flat();
// C7 (L1, L23) composes existing board regions; AxApp contains no models, announcements or legal screen to compare.
// #816 G4 defers endpoint speed, quality score and source, so these fields appear in neither model list nor detail.
export const rulings: Ruling[] = [
  ...shell,
  ...signInRulings,
  ...siteScreens.flatMap((scene) => screenFrame.map((ruling) => inScene(scene, ruling))),
  ...pageScenes.flatMap((scene) => frame.map((ruling) => inScene(scene, ruling))),
  ...presetScenes.map((scene) =>
    inScene(scene, {
      basis: "#816: there is no monthly use count for request presets",
      board: { selector: ".p-presets .ppcnt" },
    }),
  ),
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
  {
    scene: "console usage",
    basis: "#816 O23: month-end forecast and budget have no basis",
    board: { selector: ".usfc" },
  },
  {
    scene: "console usage",
    basis: "#816 O23: today/future dashed projection bars have no basis",
    board: { selector: ".usfut" },
  },
  {
    scene: "console usage",
    basis: "G1: the usage page adds hour/day/week granularity and workspace grouping controls",
    ...both(".usc .qch .usct:nth-child(2)"),
  },
  {
    scene: "console usage",
    basis: "G1: live API comparison text, KPI values, series bars, table rows and movers replace board demo numbers",
    ...both(
      ".uscmp, .usm b, .usm em, .usch svg rect, .usch .usgl, .usch .usyl > span, .usch .usxl > span, .ust tbody, .ust tfoot, .ustk .usbar, .ustk .usrw > span:nth-of-type(2), .ustk .usrw em, .ustk p, .usd .usrw",
    ),
  },
  { scene: "console calls", basis: "#816: no live mode on calls", board: { selector: ".p-logs .lglive" } },
  {
    scene: "console calls",
    basis: "§5.4: Chinese at 500 is drawn at 400 until the coverage crawl",
    ...both(".qph h1"),
  },
  {
    scene: "console calls",
    basis: "#816 O24: no slow-than-5s quick filter",
    board: { selector: '.p-logs [data-a="lst"][data-i="3"]' },
  },
  {
    scene: "console calls",
    basis: "#816 / C2: view switcher, export action, API filters, aggregates and API rows replace board demo data",
    board: { selector: ".p-logs .wv.w2" },
    page: { selector: ".p-logs .qpa, .p-logs .lgv, .p-logs .qcard" },
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
  // Links and controls with nowhere to go yet are left out until their pages come: the docs page. The rows they sat in
  // close up.
  {
    scene: "site home",
    basis: "文档页尚无，#816",
    board: { selector: ".bar .nav.mid, .hs > .main .cta" },
    page: { selector: ".bar .nav.mid, .hs > main .cta" },
  },
  // The connect screen (S2c): the docs link is not there (#816), the address in each tool's configuration is the site's,
  // and the machine is drawn by the page from its own SVG and moves on its own (fx.spec.ts).
  {
    scene: "site connect",
    basis: "文档页尚无，#816",
    board: { selector: ".hfm1 .intro .more" },
  },
  {
    scene: "site connect",
    basis: "the address is the site's own API address, not the board's example",
    ...both(".hfm1 .adr"),
  },
  {
    scene: "site connect",
    basis: "the call the card names is the one the script is on at that moment (fx-screens.spec.ts reads it)",
    ...both(".hfm1 .rcard .rc-m, .hfm1 .rcard .rc-p"),
  },
  {
    scene: "site connect",
    basis:
      "§8.2: the board's machine is drawn by its script; the page's by the fetched figure, both moving on their own",
    ...both(".hfm1 .fig, .hfm1 .wire > *"),
  },
  // The questions (S2b): the board's docs and ticket links, and its ninth question about invoices and contracts, which
  // the product does not do (docs/frontend.md「不做」); the address in the tools' settings is the site's own.
  {
    scene: "site faq",
    basis: "#816: no docs page, and the product has no tickets",
    board: { selector: ".hfm5 .hd .links" },
  },
  {
    scene: "site faq",
    basis: "docs/frontend.md「不做」: invoices, contracts and bank transfer are not in the product",
    board: { selector: ".hfm5 .qs > .q:nth-child(10), .hfm5 .qs > .fq:nth-child(9)" },
  },
  {
    scene: "site faq",
    basis:
      "docs/product.md「售卖形式」: the site shows no source labels, has no official-only switch and no price-change time",
    board: { selector: ".hfm5 .w1 .rt .tag, .hfm5 .w1 .rth > span:nth-child(2), .hfm5 .w1 .wf, .hfm5 .w5 .wf" },
  },
  {
    scene: "site faq",
    basis: "§5.4: Chinese at 500 is drawn at 400 until the coverage crawl",
    ...both(".hfm5 .qq"),
  },
  {
    scene: "site faq",
    basis: "the address is the site's own API address, not the board's example",
    ...both(".hfm5 .w2 .ac"),
  },
  // The sea bed and the page's footer (S2b): what the site has no page for is left out (#816), with the board's demo ICP
  // numbers; the legal line carries the credit for the Chinese font (docs/frontend.md「字体」); the address is the site's.
  {
    scene: "site bed",
    basis: "#816: no docs page",
    board: { selector: ".hfm6 .cta .btn.outline" },
  },
  {
    scene: "site bed",
    basis: "#816: no docs, API reference, changelog, status, tickets, contact, terms or privacy pages",
    board: { selector: ".hfm6 .fcols nav.fc:nth-of-type(n + 2), .hfm6 .fc > a:nth-of-type(3)" },
  },
  {
    scene: "site bed",
    basis: "the legal line is the site's credit for MiSans; the board's demo ICP numbers are not the site's",
    ...both(".hfm6 .legal"),
  },
  {
    scene: "site bed",
    basis:
      "docs/frontend.md「对比度令牌」: the chosen language and appearance carry a 3:1 control edge, where the board's hairline is 1.3:1",
    ...both(".hfm6 .seg .sg.on"),
  },
  {
    scene: "site bed",
    basis: "the address is the site's own API address, not the board's example",
    ...both(".hfm6 .pr2 .pv"),
  },
  {
    scene: "site bed",
    basis: "§8.2: engines are off on the board; the dune is drawn by script and tested by fx.spec.ts",
    board: { canvas: "canvas.art" },
    page: { canvas: "canvas.art" },
  },
  {
    scene: "site models",
    basis: "S4: compare the public title and lede with the board typography, without the demo sea or figures",
  },
  {
    scene: "site models",
    basis: "S4: the table, details and filter controls are the shared ModelCatalog and ModelFilterForm",
    board: { selector: ".flt > button, .lst > :not(.flt), .pnl" },
    page: { selector: ".flt > form, #model-results" },
  },
  {
    scene: "site models",
    basis: "S4: SiteShell supplies the header, footer and surface, with the site's MiSans body weight",
    page: { selector: "header, footer" },
  },
];
