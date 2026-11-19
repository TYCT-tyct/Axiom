import type { Region } from "../regions.ts";
import { entrance, runTime } from "./site-screens-a.ts";

// The connect screen (#747 S2c), cut from the Site board (.hfm1) and the SitePhone board (whose markup the tablets
// repeat). The isometric machine in its .fig is not in the cut: the board's script inserts it at run time, and the page
// fetches it as one SVG when the screen nears the view (home/connect-figure.svg), so its rules are kept by the classes
// it carries.
const range = (n: number) => Array.from({ length: n }, (_, i) => i);
const lines = 9;
const lineStates = ["cm", "n", "ad", "rn", "off", "pl"];
const classes: Record<string, string[]> = {
  m1_fig: ["A"],
  m1_noteCls: ["n-card"],
  m1_in: ["in"],
  ...Object.fromEntries(range(5).map((i) => [`m1_tab${i}`, ["on"]])),
  ...Object.fromEntries(
    range(lines).flatMap((i) => [
      [`m1_l${i}c`, lineStates],
      [`m1_l${i}K`, ["on", "wait"]],
      ...range(3).map((j) => [`m1_l${i}k${j}`, ["adr"]]),
    ]),
  ),
};
// The classes of the machine's parts and of what the script draws into it.
const inserted = [
  "iso-svg",
  "iso-solid",
  "iso-fill",
  "iso-shade",
  "iso-top",
  "iso-line",
  "iso-edge",
  "iso-crease",
  "iso-bevel",
  "iso-halo",
  "iso-dots",
  "iso-face-text",
  "mod",
  "pin",
  "band",
  "lamp",
  "mlamp",
  "dname",
  "vname",
  "lvl",
  "tb",
  "disp-",
  "ax-",
  "pulse",
];

const english: Record<string, string> = {
  m15e09730c0: "Change one address",
  me2c2f3d5e7: "Choose a tool",
  mda83883e17: "Other tools",
  m89e8fb3a55: "Get a key",
};

const connect = (board: string): Region => ({
  board,
  name: "Connect",
  roots: [".under > .hfm1"],
  // the hidden canvas, the detail views (the board's other way of showing the pick), and the docs link (#816)
  omit: [".hfm1 .frame > canvas", ".hfm1 .loupe", ".hfm1 .intro .more"],
  // the key's place in each line of the configuration is the way to sign up
  attributes: { signUpHref: { selector: ".hfm1 .kc", attribute: "href" } },
  // the comma after the first phrase is the language's: the Chinese keeps the board's kern, the English writes its own;
  // the heading's promise and the lede are the page's words, which name the site and hold for a key that may use only
  // some models (site/src/locales, script.connect)
  text: { comma: ".hfm1 h2 .kern", every: ".hfm1 h2 > .ph:last-child", lede: ".hfm1 .intro p" },
  elements: entrance("hfm1", "m1_in"),
  classes,
  inserted,
  states: runTime,
  english,
});

/** @public loaded by regions.ts from the directory listing */
export default ["Site", "SitePhone"].map(connect);
