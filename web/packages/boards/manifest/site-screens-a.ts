import type { Region } from "../regions.ts";

// Three of the home page's screens below the first (#747 S2a), as the Site board draws them inside its scroller and the
// SitePhone board (whose markup the tablets repeat) does for the narrow layout: models and routes (.hfm2), the three
// steps (.hfm3) and what to settle before production (.hfm4). Each screen's script writes its holes (values); the page
// runs the board's script on the shared frame loop (site/src/home/screens). The canvases each screen draws its own sea
// on are the board's stand-ins (its screens below share the first screen's sea) and are left out.
const range = (n: number) => Array.from({ length: n }, (_, i) => i);

// The states the scripts write into each class hole, so the region keeps the rules that select them.
const modelClasses: Record<string, string[]> = {
  ...Object.fromEntries(range(5).map((i) => [`m2_f${i}`, ["on"]])),
  ...Object.fromEntries(range(6).map((k) => [`m2_r${k}c`, ["on", "off"]])),
  ...Object.fromEntries(range(4).map((j) => [`m2_u${j}c`, ["chosen", "down", "off"]])),
  ...Object.fromEntries(range(4).flatMap((j) => range(24).map((q) => [`m2_q${j}_${q}`, ["w", "x"]]))),
};
const stepClasses: Record<string, string[]> = {
  m3_kBtn: ["press"],
  m3_kRow: ["off"],
  m3_kCp: ["on"],
  m3_kDot: ["on"],
  m3_cU0: ["del"],
  m3_cK0: ["del"],
  m3_cU1: ["off"],
  m3_cK1: ["off"],
  m3_rCls: ["ok"],
  m3_rKv: ["off"],
  ...Object.fromEntries(range(3).map((i) => [`m3_s${i}`, ["on", "done"]])),
};
const trustClasses: Record<string, string[]> = {
  ...Object.fromEntries(range(3).map((i) => [`m4_r${i}`, ["off", "pick"]])),
  m4_tgA: ["on"],
  ...Object.fromEntries(range(4).map((i) => [`m4_g${i}c`, ["new", "retry", "old", "off"]])),
  m4_kBar: ["full"],
  m4_kDot: ["warn"],
  m4_kTxtC: ["warnT"],
};

const modelsEnglish: Record<string, string> = {
  m78d48a1986: "One model, more than one route behind it",
  m46cfd15f68: "One model",
  m5378bf24f6: "more than one route behind it",
  maa92be6c44:
    "Every model is served over several routes. Axiom picks one by price and availability, and when one fails, it moves to the next.",
  m7b00c3d69a: "models",
  m2da0011d24: "routes",
  m16ece33056: "tokens, last 7 days",
  m39a1bf8393: "Filter by developer",
  m5c55a67935: "All",
  m86ecb15df1: "China",
  mc98e118e0a: "Model",
  m13b822effa: "From, per million input tokens",
  m4f359ce335: "See all ",
  m5ad00154b5: " models",
  m47f7f8fd52: "Context ",
  mdada2a05f1: "Max output ",
  m223b24b821: "Route",
  m2087c777c0: "Input",
  mfb04addb4c: "Output",
  mddab042697: "First token",
  m51694b10eb: "7-day uptime",
  me4a79af431: "Route ",
  m0b65c79cc5: "Updated ",
};
const stepsEnglish: Record<string, string> = {
  m660c2c2cea: "Three steps from sign-up to your first request",
  mac86e0f670: "Keep the SDK you use now and change two lines of configuration.",
  mcaf724c6bd: "Sign up and get an API key",
  m099fc1e8b6: "Create key",
  md44e9b3d3b: "Name",
  m000f03156e: "Models",
  m5c55a67935: "All",
  m07ec86e0f1: "Created",
  mc98e118e0a: "Model",
  m223b24b821: "Route",
  mddab042697: "First token",
  mc25db45edc: "Charged",
  m990d2e8e40: "Sign up and create a key",
  m40981b6b53:
    "After signing up, create a key in the console. One key per project keeps its limits and its bill apart from the rest.",
  m0e0114b471: "Swap the address and the key",
  ma44b73539e: "Point base_url at Axiom and set api_key to the new key. Nothing else changes.",
  md932eaf1ce: "Send your first request",
  m01c1e11fcc:
    "Set model to a name from the models page. The console records which route each request took and what it cost.",
};
const trustEnglish: Record<string, string> = {
  m9b68aa6c7d: "Before production, settle these first",
  m27dffa08b5: "Before production, ",
  mc6aef08746: "settle these first",
  m90a20e4359: "Whose route it is, how you are charged, and what to do when a key leaks.",
  md3a84a17e9: "Input / million tokens",
  mb770360c59: "Official",
  m376cbd8cfc: "Third party",
  m10253bf9e7: "Official routes only",
  m6be861a9e1: "Every route names its source",
  m30a6e561de:
    "The models page says whether a route is the official API or a third party. To stay off third parties, turn on \u201cOfficial routes only\u201d; the price is a little higher.",
  m8b6ff49851: "Time",
  mc98e118e0a: "Model",
  m223b24b821: "Route",
  mc25db45edc: "Charged",
  m8d580f9d86: "This month",
  m28e7909661: "One line per request",
  m9ee5734476: "Monthly limit",
  m000f03156e: "Models",
  m95ed54ee52: "IP allowlist",
  mad99584991: "Every key has its own ceiling",
  mcb7f6ab708:
    "Set a monthly limit, the models and the IPs that may call, key by key. When the limit is reached the key stops, and nothing more is charged.",
};

// A screen fades in the first time it is seen (the board's .hfm.in); the script sets the hole.
const entrance = (screen: string, hole: string) => ({
  [`.under > .${screen}`]: { set: { class: `hfm ${screen} {{${hole}}}` } },
});
const pressable = { set: { tabindex: "0", role: "button" } };

const screens = (board: string): Region[] => [
  {
    board,
    name: "Models",
    roots: [".under > .hfm2"],
    omit: [".frame > canvas"],
    // the title is the page's: the board's phrases in Chinese (ModelsTitle), plain text in English; the list and the
    // panel are ModelsRow, which the page swaps for a state while the models are loading, failed or none
    slots: { title: ".hfm2 .hl h2", row: ".hfm2 .sec > .row" },
    // the card naming the pick, left out while no route is picked
    elements: { ...entrance("hfm2", "m2_in"), ".hfm2 .frame > .pick": { unless: "m2_noPick" } },
    classes: { m2_pickCls: ["fail"], m2_in: ["in"] },
    english: modelsEnglish,
  },
  {
    board,
    name: "ModelsRow",
    roots: [".hfm2 .sec > .row > *"],
    // the API publishes no usage, so the list has no trend to show (port design R14)
    omit: [".mr > .mt"],
    // a model is chosen from the keyboard as well (the script turns Enter and Space into its click)
    elements: { ".mr": pressable },
    // the prices are in the site's currency, which the page names
    text: { unit: ".pf > span:first-child" },
    attributes: { moreHref: { selector: ".more", attribute: "href" } },
    classes: modelClasses,
    english: modelsEnglish,
  },
  {
    board,
    name: "Steps",
    roots: [".under > .hfm3"],
    // the docs page comes with S4
    omit: [".frame > canvas", ".cta .outline"],
    attributes: { signUpHref: { selector: ".cta .primary", attribute: "href" } },
    elements: { ...entrance("hfm3", "m3_in"), ".hfm3 div.st": pressable },
    classes: { ...stepClasses, m3_in: ["in"] },
    english: stepsEnglish,
  },
  {
    board,
    name: "Trust",
    roots: [".under > .hfm4"],
    // paying as a company (bank transfer, invoices) is not in the product (docs/frontend.md「不做」)
    omit: [".frame > canvas", ".ent"],
    // the ledger card's sentence is the page's: who is not charged is the billing rule's, not the board's wording
    text: { ledgerNote: ".cards > article:nth-child(2) > p" },
    elements: entrance("hfm4", "m4_in"),
    classes: { ...trustClasses, m4_in: ["in"] },
    english: trustEnglish,
  },
];

// The models screen's title in Chinese: two phrases that keep together, the comma between them kerned.
const title: Region = { board: "Site", name: "ModelsTitle", roots: [".hfm2 .hl h2 > .ph"], english: modelsEnglish };

// The box the screens below the first sit in, whose class their rules select under; the page puts its screens in it.
const under: Region = { board: "Site", name: "Under", roots: [".hs > .under"], children: ".hs > .under" };

/** @public loaded by regions.ts from the directory listing */
export default [...["Site", "SitePhone"].flatMap(screens), title, under];
