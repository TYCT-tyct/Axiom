import type { Region } from "../regions.ts";
import { entrance, pressable } from "./site-screens-a.ts";

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
  m5ad3e46da2:
    "One key calls every model, with no separate account or top-up for each vendor. Behind one model there are several routes: Axiom picks one by price and availability and moves to the next when one fails. There is one bill.",
  m0a6b58d599:
    "A route the model\u2019s maker does not run itself, usually cheaper than the official one. The models page says whether each route is the official API or a third party. If your data is sensitive, turn on \u201cOfficial routes only\u201d in settings.",
  m580f9d34c3:
    "Claude Code, Codex, Cursor and the OpenAI SDK all work as they are: change the address and the key. So does any other tool that speaks the OpenAI or Anthropic API.",
  m7908066605:
    "Billing is per token. Each route has its own price, and you pay the price of the route that served the request. The models page shows the lowest price across all routes.",
  m59549165d5:
    "Axiom retries on another route. The failed attempt is not charged, and the console shows it together with the retry.",
  m9799ce84e2:
    "Route prices follow the upstream. The models page shows when a price was last updated, and each charge uses the price at the moment the request was sent.",
  m475efcf60a:
    "Delete the key in the console and create a new one. Set a spending limit and an IP allowlist on every key in advance, and a leak can only cost so much.",
  ma60cdd302f: "Signing up is free. There is no trial credit for now: you pay by usage, only for what you use.",
  mab79bd5670: "Model / claude-sonnet-5.5",
  mf850f24755: "Model / claude-sonnet-5.5 / Price",
  m223b24b821: "Route",
  ma488e93d69: "Source",
  mf2355ab114: "Input / million tokens",
  mb770360c59: "Official",
  m376cbd8cfc: "Third party",
  m10253bf9e7: "Official routes only",
  m157a297814: "# Change these two lines, nothing else",
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
  m649a0fc723: "Free",
  m690c02e414: "Trial credit",
  mf127804b92: "None",
  m55d7debcab: "Billing method",
  m2fe69c47c2: "Per token used",
};
const bedEnglish: Record<string, string> = {
  m4ba35a8f55: "Models change, ",
  m46ff80e47d: "integration stays the same",
  m96a055f345: "Sign up for a key, change two lines of configuration, and keep using the tools you use now.",
  mcaf724c6bd: "Sign up and get an API key",
  mc98e118e0a: "Model",
  m4606318793: "changes",
  mf3b3924597: "Integration",
  m494bdd740c: "stays",
  m53590c0067: "Swapped ",
  m5ad00154b5: " models",
  mc226e9802b: "Code changed ",
  m05198195b9: " lines",
  m4ab0ec9fb8: "One key for every model.",
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
      // the ninth question, its pane and its patch point
      ...(wide
        ? [".hfm5 .qs > .q:nth-child(10)", ".hfm5 .card > .pane:nth-child(9)", ".hfm5 .patch > .pad:nth-child(9)"]
        : [".hfm5 .qs > .fq:nth-child(9)"]),
    ],
    // the address the reader's tools are set to is the site's own
    text: { apiRoot: ".hfm5 .w2 .cl:nth-child(2) > .ac", apiV1: ".hfm5 .w2 .cl:nth-child(3) > .ac" },
    elements: entrance("hfm5", "m5_in"),
    classes: faqClasses,
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
  text: { credit: ".hfm6 .legal > span:first-child", apiV1: ".hfm6 .pr2 .pv" },
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
  english: bedEnglish,
});

/** @public loaded by regions.ts from the directory listing */
export default ["Site", "SitePhone"].flatMap((board) => [faq(board), bed(board)]);
