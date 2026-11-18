import type { Region } from "../regions.ts";
import shell from "./shell.ts";

// The admin's shell is the console's (shell.ts) until the admin gets its own design: the same bar, sidebar and phone
// tabs, without what only the console has (the balance, help, the path, the account's pages), and with the navigation
// lists as slots the admin fills with its own pages. Each entry it puts there is one of the board's own entries
// (AdminNav, AdminTab, AdminMore below), so it keeps the board's look and icons.
const consoleOnly = [
  ".g11r > .pw:has(> .gbal)",
  ".pw:has(> .ghlp)",
  ".gbar .qbc",
  ".pnM",
  ".qme > .pmi:is([data-pg=profile], [data-pg=security], [data-pg=notify])",
];
// The console's changes to what is left out above or emptied for a slot; the rest hold for the admin as they are.
const notAdmin = /\.gbal|\.bp\b|\.ghlp|\.qhlp|\.qbc\b|\.g11r > \.pw|\.pmi\[data-pg|\.qnav\[|\.otabs > a|\.qmi\[/;

// The drawing's navigation lists, which the demo shows in the slots, in English.
const slotEnglish = {
  m12faea6473:
    "OverviewUsageLogsAPI Keys{{mK}}RoutingPresetsPluginsBring your own keyGuardrails and budgetsObservabilityBilling and top-upManagement keys",
  m680d9c1cba: "OverviewUsageLogsKeysMore",
  m794dc1bb73:
    "RoutingPresetsPluginsBring your own keyGuardrails and budgetsObservabilityBilling and top-upManagement keys",
};

function adminShell({ elements = {}, omit = [], text = {}, ...region }: Region, slots: Record<string, string>): Region {
  return {
    ...region,
    english: { ...region.english, ...slotEnglish },
    name: "AdminShell",
    omit: [...omit, ...consoleOnly],
    slots: { ...region.slots, ...slots },
    text: { language: text.language as string },
    elements: Object.fromEntries(Object.entries(elements).filter(([selector]) => !notAdmin.test(selector))),
  };
}

// A navigation entry: all the entries the board draws in one list, of which the admin shows the one whose icon it picks
// (not_<page> false) as a link to its own page, with its own label, and `on` while that page is open.
type Entry = { scope: string; pages: string[]; label: string; current: string };
function entry(board: string, name: string, { scope, pages, label, current }: Entry, more = {}): Region {
  return {
    board,
    name,
    roots: [scope],
    omit: [".nm"],
    slots: { label },
    english: wide.english,
    elements: {
      ...Object.fromEntries(
        pages.map((pg) => [
          `${scope}[data-pg=${pg}]`,
          { tag: "link", set: { href: "{{to}}", class: current }, unless: `not_${pg}` },
        ]),
      ),
      ...more,
    },
  };
}

const [wide, phone] = shell as [Region, Region];
const listed = ["route", "presets", "plugins", "byok", "budget", "observe", "bill", "mkeys"];

/** @public loaded by regions.ts from the directory listing */
export default [
  adminShell(wide, { nav: ".pnP .qnv" }),
  adminShell(phone, { tabs: ".otabs", more: ".qmore .qmg" }),
  entry("AxApp", "AdminNav", {
    scope: ".pnP .qnav",
    pages: ["ov", "usage", "logs", "keys", ...listed],
    label: ".pnP .qnav .nl",
    current: "ni qnav {{on}}",
  }),
  // The tabs' last entry is the button that opens the sheet of the rest.
  entry(
    "AxAppPhone",
    "AdminTab",
    {
      scope: ".otabs.onPW > a",
      pages: ["ov", "usage", "logs", "keys"],
      label: ".otabs.onPW > a > span",
      current: "{{on}}",
    },
    {
      ".otabs.onPW > a:not([data-pg])": {
        set: { role: "button", tabindex: "0", onkeydown: "{{press}}", "aria-expanded": "{{moreOpen}}" },
        unless: "not_more",
      },
    },
  ),
  entry("AxAppPhone", "AdminMore", {
    scope: ".qmore > .onP .qmi",
    pages: listed,
    label: ".qmore > .onP .qmi > span",
    current: "qmi {{on}}",
  }),
];
