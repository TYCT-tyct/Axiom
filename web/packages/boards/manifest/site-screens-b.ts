import type { Region } from "../regions.ts";
import { entrance, pressable, runTime } from "./site-screens-a.ts";

// Two more of the home page's screens below the first (#747 S2b), cut as site-screens-a.ts cuts its three: the
// questions (.hfm5) and the sea bed with the page's footer (.hfm6), from the Site board and the SitePhone board.
const range = (n: number) => Array.from({ length: n }, (_, i) => i);

// The questions are eight: the ninth (invoices, contracts, bank transfer) is not in the product (docs/frontend.md「不做」).
const questions = 8;
const faqClasses: Record<string, string[]> = {
  ...Object.fromEntries(range(questions).map((i) => [`m5_o${i}`, ["on"]])),
  ...Object.fromEntries(range(4).map((i) => [`m5_v${i}`, ["on"]])),
  m5_in: ["in"],
};
const bedClasses: Record<string, string[]> = {
  m6_zhOn: ["on"],
  m6_enOn: ["on"],
  m6_sunOn: ["on"],
  m6_moonOn: ["on"],
  m6_in: ["in"],
};

const faqEnglish: Record<string, string> = {
  m45a6d115fd: "Frequently asked questions",
  md099384653: "How is this different from using the official API directly?",
  m809ecaafa7: "What is a \u201cthird-party route\u201d?",
  mf5debeb346: "Which tools and SDKs work?",
  mfe455e5708: "How does billing work?",
  m658a95c942: "What happens when a request fails?",
  m728483a98b: "Why do prices change?",
  mb3e6985586: "What if my key leaks?",
  m6a3fc5442c: "Does signing up cost anything?",
  mab79bd5670: "Model / claude-sonnet-5.5",
  mf850f24755: "Model / claude-sonnet-5.5 / Price",
  m223b24b821: "Route",
  ma488e93d69: "Source",
  mf2355ab114: "Input / million tokens",
  mb770360c59: "Official",
  m376cbd8cfc: "Third party",
  m10253bf9e7: "Official routes only",
  m0794deeb5d: "Billing / request details",
  m2087c777c0: "Input",
  mfb04addb4c: "Output",
  m973e96ef3f: "Total, route B",
  med76c43387: "Unit prices are per million tokens",
  m4bf3c13e58: "Billing",
  m8b6ff49851: "Time",
  mc98e118e0a: "Model",
  mc25db45edc: "Charged",
  m96572e0d27: "B timed out",
  mf56da483c2: "Not charged",
  m265189cf0a: "The same request, moved to route A automatically",
  m9b2e0af217: "Route B input price, last 7 days",
  m1d55e968dd: "Updated today 10:20",
  m077a6d3771: "Deleted",
  m86f27b2c78: "Just created",
  mcf8ebb7d17: "Limit \u00a5500/month",
  m061246bd31: "Account",
  mc4fb62202b: "Sign-up",
  m690c02e414: "Trial credit",
  mf127804b92: "None",
  m55d7debcab: "Billing method",
};
const bedEnglish: Record<string, string> = {
  m4ba35a8f55: "Models change, ",
  m46ff80e47d: "integration stays the same",
  mc98e118e0a: "Model",
  m4606318793: "changes",
  mf3b3924597: "Integration",
  m494bdd740c: "stays",
  m53590c0067: "Swapped ",
  m5ad00154b5: " models",
  mc226e9802b: "Code changed ",
  m05198195b9: " lines",
  maea82737cc: "Product",
  m0f4371a7a9: "Pricing",
  m9f6fee1aba: "Language",
  m86a63f23a0: "Appearance",
  m72726d8818: "\u4e2d\u6587",
  maa0819dfc4: "Light",
  ma6b75d0680: "Dark",
};

// In this screen's own window headers .wh is a class of its own; the sea's whale canvas is the frame's .tr > .wh, so
// every selector here starts from the screen.
const faq = (board: string): Region => {
  const wide = board === "Site";
  return {
    board,
    name: "Faq",
    roots: [".under > .hfm5"],
    omit: [
      ".hfm5 .frame > canvas",
      // the docs page does not exist (#816) and the product has no tickets
      ".hfm5 .hd > .links",
      // the source tags and the official-only switch of the second picture, and the last-change line of the sixth: the
      // site shows no source label, has no such switch, and no change time (docs/product.md「售卖形式」)
      ".hfm5 .w1 .rt .tag",
      ".hfm5 .w1 .rth > span:nth-child(2)",
      ".hfm5 .w1 .wf",
      ".hfm5 .w5 .wf",
      // the ninth question, its pane and its patch point
      ...(wide
        ? [".hfm5 .qs > .q:nth-child(10)", ".hfm5 .card > .pane:nth-child(9)", ".hfm5 .patch > .pad:nth-child(9)"]
        : [".hfm5 .qs > .fq:nth-child(9)"]),
    ],
    // the address the reader's tools are set to is the site's own; the answers are the page's words, which say what
    // the product does today (script.faq), and so is the last picture's billing line, as not every endpoint bills by
    // the token (docs/domains/billing.md「计费项」)
    text: {
      ans: ".hfm5 .ans",
      apiRoot: ".hfm5 .w2 .cl:nth-child(2) > .ac",
      apiV1: ".hfm5 .w2 .cl:nth-child(3) > .ac",
      billing: ".hfm5 .w7 .kv:last-child > .t1",
      // signing up, as this site does it (getSite's registration)
      signUp: ".hfm5 .w7 .kv:first-child > .t1",
      // what the tools' picture shows: the two addresses, not the whole configuration
      envNote: ".hfm5 .w2 .cl:first-child > .cm",
    },
    elements: {
      ...entrance("hfm5", "m5_in"),
      // without the site's address the picture shows no command, only that it is unavailable
      ".hfm5 .w2 .cl:nth-child(2)": { unless: "m5_noApi" },
      ".hfm5 .w2 .cl:nth-child(3)": { unless: "m5_noApi" },
      // in the accordion a closed answer is clipped, not gone: the expanded state takes it out of what is read too; in
      // the wide layout the questions are tabs to the keyboard as the ARIA pattern has them, one stop for the row, the
      // arrows moving along it, each controlling its pane
      ...(wide
        ? {
            ".hfm5 .qs": { set: { onkeydown: "{{m5_tabKey}}" } },
            ...Object.fromEntries(
              range(questions).flatMap((i) => [
                [
                  `.hfm5 .qs > .q:nth-child(${i + 2})`,
                  { set: { id: `faq-tab-${i}`, tabindex: `{{m5_ti${i}}}`, "aria-controls": `faq-pane-${i}` } },
                ],
                [
                  `.hfm5 .card > .pane:nth-child(${i + 1})`,
                  { set: { id: `faq-pane-${i}`, "aria-labelledby": `faq-tab-${i}` } },
                ],
              ]),
            ),
          }
        : Object.fromEntries(
            range(questions).map((i) => [
              `.hfm5 .qs > .fq:nth-child(${i + 1}) > .fqa`,
              { set: { "aria-hidden": `{{m5_h${i}}}` } },
            ]),
          )),
    },
    classes: faqClasses,
    states: runTime,
    english: faqEnglish,
  };
};

// The board's own footer is the page's: the site's name and mark stay, the columns whose pages do not exist
// (docs, API reference, changelog, status, tickets, contact, terms, privacy; no ICP numbers) are left out, and the
// credit for the Chinese font stays in the legal line (docs/frontend.md「字体」).
const bed = (board: string): Region => ({
  board,
  name: "Bed",
  roots: [".under > .hfm6"],
  omit: [
    ".hfm6 .frame > canvas[hidden]",
    ".hfm6 .cta .btn.outline",
    ".hfm6 .fcols > nav.fc:nth-of-type(n + 2)",
    ".hfm6 .fcols > nav.fc > a:nth-of-type(3)",
    ".hfm6 .legal > span:nth-child(n + 2)",
  ],
  // the site's own name and logo take the brand's place, all of it: the board's mark and its bare "Axiom" text
  slots: { brand: ".hfm6 .ft .brand" },
  // the way in and the line before it say how this site takes new users, and the brand's line holds for a key that may
  // use only some models: the page's words (site/src/locales, script.bed)
  text: {
    credit: ".hfm6 .legal > span:first-child",
    apiV1: ".hfm6 .pr2 .pv",
    lede: ".hfm6 .el > .lede",
    join: ".hfm6 .cta .btn.primary",
    tagline: ".hfm6 .ft .fb > p",
  },
  attributes: {
    signUpHref: { selector: ".hfm6 .cta .btn.primary", attribute: "href" },
    homeHref: { selector: ".hfm6 .ft .brand", attribute: "href" },
    modelsHref: { selector: ".hfm6 .fc > a:nth-of-type(1)", attribute: "href" },
    pricingHref: { selector: ".hfm6 .fc > a:nth-of-type(2)", attribute: "href" },
  },
  elements: {
    ...entrance("hfm6", "m6_in"),
    ".hfm6 .seg .sg": pressable,
    ".hfm6 .seg:nth-child(1) > .sg:nth-child(1)": {
      set: { class: "sg {{m6_zhOn}}", "aria-pressed": "{{m6_zhP}}", "data-pick": "zh" },
    },
    ".hfm6 .seg:nth-child(1) > .sg:nth-child(2)": {
      set: { class: "sg {{m6_enOn}}", "aria-pressed": "{{m6_enP}}", "data-pick": "en" },
    },
    ".hfm6 .seg:nth-child(2) > .sg:nth-child(1)": { set: { "aria-pressed": "{{m6_sunP}}", "data-pick": "light" } },
    ".hfm6 .seg:nth-child(2) > .sg:nth-child(2)": { set: { "aria-pressed": "{{m6_moonP}}", "data-pick": "dark" } },
  },
  classes: bedClasses,
  states: runTime,
  english: bedEnglish,
});

/** @public loaded by regions.ts from the directory listing */
export default ["Site", "SitePhone"].flatMap((board) => [faq(board), bed(board)]);
