import type { Region } from "../regions.ts";

// The parts of the design system board (AxDS) that pages without a board of their own are made of; packages/boards/ds.tsx
// gives each a typed component. A part is cut from the board's own drawing of it, and what the page supplies (titles,
// options, cells, rows) replaces the drawing's sample content. Its rules select under .ax, which the shell's board root
// carries. Only what a page uses is cut: a part no page uses is left in the board until one does.

// The board's sample content, which the demo shows in the slots and rows, in English.
const english = {
  m53fa27b069: "Axiom console design system",
  m98738462d5:
    "Gentle, quiet, easy to use. Every item below is a real component you can press; the properties on the right switch between light, dark and reduced motion.",
  m070a8016fc: "Colour",
  m279c381531:
    "Warm grey Sand is the neutral, the two sets matching level for level. Cyan is only for the selected, the live and Axiom's choice; state colours appear only on states.",
  m0eeecd26f2: "This month",
  m84ad2952a3: "30 days",
  m38eefacbb3: "7 days",
  mda6a376483: "NameKeyThis month",
};

// The board's amount cell, by its place: its class is the page's.
const cell = ".xrs > .xr:nth-child(2) > span:nth-child(3)";

const parts: Region[] = [
  // The board's own title block: the page's h1 and the sentence under it.
  {
    board: "AxDS",
    name: "PageHeader",
    roots: [".dtop"],
    slots: { title: ".dtop h1", description: ".dtop p" },
  },
  // A block's heading and its note, as the board heads each of its sections.
  {
    board: "AxDS",
    name: "SectionHeader",
    roots: [".dsb > .sec2:first-of-type > .bh"],
    slots: { title: ".dsb > .sec2:first-of-type > .bh h2", description: ".dsb > .sec2:first-of-type > .bh p" },
  },
  // A choice of three options, as the board draws it: its buttons' own state and press holes (sg0, sgs0…), the sliding
  // mark's place (sg), and the pressed state for screen readers.
  {
    board: "AxDS",
    name: "Segmented",
    roots: [".seg"],
    text: { option: ".seg > button" },
    elements: {
      ".seg": { set: { role: "group", "aria-label": "{{label}}" } },
      ...Object.fromEntries(
        [0, 1, 2].map((i) => [`.seg > button:nth-of-type(${i + 1})`, { set: { "aria-pressed": `{{pressed${i}}}` } }]),
      ),
    },
  },
  // A table: the heading row and the rows of the page, each a row of cells laid out on the columns the page gives. The
  // board's list is a fixed window for its slide-in demo; a page's list is as long as its rows (bodyStyle).
  {
    board: "AxDS",
    name: "Table",
    roots: [".tb"],
    slots: { head: ".tb > .xh", children: ".xrs" },
    elements: {
      ".tb": { set: { role: "table", "aria-label": "{{label}}" } },
      ".tb > .xh": { set: { role: "row", style: "{{columns}}" } },
      ".xrs": { set: { class: "xrs", role: "rowgroup", style: "{{bodyStyle}}" } },
    },
  },
  {
    board: "AxDS",
    name: "Row",
    roots: [".xrs > .xr:nth-child(2)"],
    children: ".xrs > .xr:nth-child(2)",
    elements: { ".xrs > .xr:nth-child(2)": { set: { class: "xr", role: "row", style: "{{columns}}" } } },
  },
  // A cell, a heading cell included: its role, and the board's kinds of cell text (num, mono, mut).
  {
    board: "AxDS",
    name: "Cell",
    roots: [cell],
    children: cell,
    elements: { [cell]: { set: { class: "{{kind}}", role: "{{role}}" } } },
    classes: { kind: ["num", "mono", "mut"] },
  },
];

type ConsolePart = Omit<Region, "board" | "render"> & { render: NonNullable<Region["render"]> };

const keyDialog = {
  root: ".fr",
  actions: [
    ["go", "keys"],
    ["nkx", {}],
  ],
} satisfies NonNullable<Region["render"]>;

const logs = { root: ".p-logs", actions: [["go", "logs"]] } satisfies NonNullable<Region["render"]>;
const personal = ".wv.w2";
const filter = `${personal} .usf`;
const chip = `${filter} .uschip[data-c=m]`;
const field = ".nkdlg .nkf:first-child";
const sort = `.p-usage ${personal} .ust th[data-a=sort][data-i="0"]`;
const skeleton = `.p-ov ${personal} .qcard.ovmc`;
const hole = (name: string) => `{{${name}}}`;

const input = {
  id: hole("id"),
  name: hole("name"),
  value: hole("initial"),
  placeholder: hole("placeholder"),
  autocomplete: hole("autocomplete"),
  "aria-describedby": hole("describedBy"),
  ref: hole("control"),
  class: "nkin {{invalidClass}}",
  oninput: hole("input"),
};

function formField(name: string, multiline: boolean): ConsolePart {
  const area = ".nkf:has(.nkcidr)";
  return {
    name,
    page: "keys",
    render: multiline
      ? { ...keyDialog, actions: [...keyDialog.actions, { click: ".nkmh" }, { click: ".nkdlg [data-a=ip]" }] }
      : keyDialog,
    roots: [multiline ? area : field],
    text: { label: "label", message: ".nkf > div:last-child" },
    omit: multiline ? [".nkrow > button"] : [],
    elements: {
      [multiline ? ".nkl" : "label"]: { tag: multiline ? "label" : undefined, set: { for: hole("id") } },
      [multiline ? "textarea" : "input"]: { set: { ...input, ...(multiline ? {} : { type: hole("inputType") }) } },
      ".nkhint": {
        set: { class: hole("messageClass"), id: hole("descriptionId") },
        unless: "hideMessage",
      },
    },
    classes: { invalidClass: ["bad"], messageClass: ["nkhint", "nkerr"] },
  };
}

const status = (name: string, kind: string, page: string, props: Record<string, string>): ConsolePart => {
  const root = `.stb.${kind}`;
  return {
    name,
    page,
    render: { root: ".stbs", actions: kind === "deny" ? [["openWs", 1, page]] : [["go", page]], props },
    roots: [root],
    text: {
      title: "h2",
      description: "p",
      primary: ".stba .pri span",
      ...(kind !== "empty" ? { secondary: ".stba .sec span" } : {}),
    },
    omit: kind === "err" ? [".stbid"] : [],
    elements: {
      [root]: { set: { role: kind === "err" ? "alert" : "status" } },
      p: { unless: "hideDescription" },
      ".stba": { unless: "hideActions" },
      ".stba .pri": { unless: "hidePrimary", set: { onclick: hole("primary"), disabled: hole("pending") } },
      ...(kind !== "empty" ? { ".stba .sec": { unless: "hideSecondary", set: { onclick: hole("secondary") } } } : {}),
    },
  };
};

const consoleParts: ConsolePart[] = [
  {
    name: "PartDialog",
    page: "keys",
    render: keyDialog,
    // Keep a neutral parent so the frame-level panel can be emitted as a native dialog.
    roots: [".fr"],
    omit: [".fr > :not(.nkscrim, .nkdlg)"],
    children: ".nkb",
    text: {
      title: ".nkh h2",
      description: ".nkh p",
      keyboardHint: ".nkfl",
      secondary: ".nkfoot .sec",
      primary: ".nkfoot [data-a=ok]",
    },
    attributes: { closeLabel: { selector: ".nkx", attribute: "aria-label" } },
    elements: {
      ".fr": { set: { class: "", style: hole("frameStyle") } },
      ".nkscrim": { set: { onclick: hole("close"), "aria-hidden": "true", style: hole("placement") } },
      ".nkdlg": {
        tag: "dialog",
        set: { ref: hole("dialog"), style: hole("placement"), "aria-labelledby": hole("titleId"), tabindex: "-1" },
      },
      ".nkh h2": { set: { id: hole("titleId") } },
      ".nkh p": { unless: "hideDescription" },
      ".nkx": { set: { onclick: hole("close") } },
      ".nkfl": { unless: "hideHint" },
      ".nkfoot .sec": {
        unless: "hideSecondary",
        set: { onclick: hole("secondary"), disabled: hole("secondaryDisabled") },
      },
      ".nkfoot .pri": {
        set: {
          class: "btn {{primaryClass}}",
          form: hole("form"),
          ref: hole("primaryButton"),
          onclick: hole("primary"),
          disabled: hole("primaryDisabled"),
        },
      },
    },
    classes: { primaryClass: ["pri", "sec", "dan2"] },
  },
  {
    name: "PartDialogSample",
    page: "keys",
    render: keyDialog,
    roots: [".nkdlg .nkb > *"],
  },
  formField("PartTextField", false),
  formField("PartTextAreaField", true),
  {
    name: "PartFilterBar",
    page: "logs",
    render: logs,
    roots: [filter],
    children: filter,
    elements: { [filter]: { set: { role: "search", "aria-label": hole("label") } } },
  },
  {
    name: "PartFilterSample",
    page: "logs",
    render: logs,
    roots: [`${filter} > *`],
  },
  {
    name: "PartSearch",
    page: "logs",
    render: logs,
    roots: [`${filter} .lgq`],
    attributes: { placeholder: { selector: ".lgqi", attribute: "placeholder" } },
    elements: {
      ".lgqi": { set: { type: "search", value: hole("initial"), "aria-label": hole("label"), oninput: hole("input") } },
    },
  },
  {
    name: "PartFilterSelect",
    page: "logs",
    render: logs,
    roots: [chip],
    children: ".uspop",
    text: { label: `${chip} > button` },
    elements: {
      [chip]: { set: { class: "uschip {{openClass}}", onkeydown: hole("keyDown"), ref: hole("select") } },
      [`${chip} > button`]: {
        set: {
          class: hole("activeClass"),
          onclick: hole("toggle"),
          "aria-expanded": hole("open"),
          "aria-controls": hole("listId"),
          "aria-label": hole("filterLabel"),
        },
      },
      ".uspop": { set: { id: hole("listId"), hidden: hole("closed") } },
    },
    classes: { openClass: ["open"], activeClass: ["on"] },
  },
  {
    name: "PartFilterOption",
    page: "logs",
    render: logs,
    roots: [`${chip} [data-a=pickm][data-i="0"]`],
    text: { label: "button" },
    elements: {
      button: { set: { class: hole("selectedClass"), onclick: hole("choose"), "aria-pressed": hole("selected") } },
      i: { set: { style: hole("color") } },
    },
    classes: { selectedClass: ["on"] },
  },
  {
    name: "PartFilterClear",
    page: "logs",
    render: logs,
    roots: [`${chip} .usclr`],
    text: { label: "button" },
    elements: { button: { set: { onclick: hole("clear") } } },
  },
  {
    name: "PartDateRanges",
    page: "logs",
    render: logs,
    roots: [`${filter} .qseg[data-s=r]`],
    text: { range: 'button[data-a="r"]' },
    elements: {
      ".qseg": { set: { style: hole("selection"), role: "group", "aria-label": hole("label") } },
      ...Object.fromEntries(
        Array.from({ length: 5 }, (_, i) => [
          `button[data-a=r][data-i="${i}"]`,
          { set: { class: hole(`active${i}`), onclick: hole(`choose${i}`), "aria-pressed": hole(`pressed${i}`) } },
        ]),
      ),
    },
  },
  {
    name: "PartLoadMore",
    page: "logs",
    render: { ...logs, actions: [...logs.actions, { click: `${filter} [data-a=r][data-i="2"]` }] },
    roots: [`${personal} .lgmore`],
    text: { label: "button" },
    elements: {
      ".lgmore": { set: { "aria-live": "polite" } },
      button: { set: { onclick: hole("load"), disabled: hole("pending") } },
    },
  },
  {
    name: "PartListEnd",
    page: "logs",
    render: logs,
    roots: [`${personal} .lgend`],
    slots: { label: ".lgend" },
    elements: { ".lgend": { set: { role: "status" } } },
  },
  {
    name: "PartSortableHeader",
    page: "usage",
    render: { root: ".p-usage", actions: [["go", "usage"]] },
    roots: [sort],
    text: { label: sort },
    elements: {
      [sort]: {
        set: {
          class: hole("selected"),
          scope: "col",
          ref: hole("header"),
          tabindex: "0",
          onclick: hole("sort"),
          onkeydown: hole("keyDown"),
        },
      },
    },
  },
  {
    name: "PartLoading",
    page: "ov",
    render: { root: ".p-ov", actions: [["go", "ov"]], props: { view: "加载中" } },
    roots: [skeleton],
    children: skeleton,
    elements: { [skeleton]: { set: { role: "status", "aria-label": hole("label"), "aria-busy": "true" } } },
  },
  {
    name: "PartLoadingSample",
    page: "ov",
    render: { root: ".p-ov", actions: [["go", "ov"]], props: { view: "加载中" } },
    roots: [`${skeleton} > *`],
  },
  status("PartEmpty", "empty", "ov", { view: "空" }),
  status("PartError", "err", "ov", { view: "出错" }),
  status("PartNoPermission", "deny", "ov", { role: "成员", team: "已有团队" }),
];

const consoleEnglish = {
  m00bd3960fe: "Expand",
  m0164f80178: "All models",
  m051425eb75: "Spending comes out of your balance",
  m0862f92077: "New API key",
  m0cdc278f81: "Use a name you will recognize in logs and usage, such as a service name.",
  m0eeecd26f2: "This month",
  m0f7d68ecdc: "Report a problem",
  m13cbd53700: "Requests using this key will be denied at the limit. The limit resets on the first of next month.",
  m19e7c0223e: "By model",
  m2261b06712: "Last 7 days",
  m27a501b31c: "Create a key to send your first request in minutes.",
  m28384d7afd: "Failed",
  m2cd0f3be87: "Cancel",
  m311fd2d94c: "Cost",
  m3fd47edce4: "Close",
  m449597f5dd: "Press Enter to create",
  m44a9811df3: "Back to workspaces",
  m472e9ec385: "Only allow these IP addresses",
  m4eafa9e925: "Custom",
  m5073e9f5ed: "See usage",
  m557056ed67: "All models, never expires, any IP",
  m5c55a67935: "All",
  m61130923cc: "Slower than 5 seconds",
  m7bee49c4a9: "Enter one address or subnet per line.",
  m7e0167c239: "Could not load overview",
  m83d1cb8172: "Ask an admin to add me",
  m8789aaa26c: "No requests yet",
  m93d69dab8c: "Enter an amount",
  m97b1b4bf1f: "You are not in Evaluation yet",
  ma24a3052bb: "Monthly limit",
  ma2872c7461: "More restrictions",
  mb2f77bc7e3: "This workspace is restricted to selected members. Ask an admin to add you.",
  mb8784c8dd5: "Retry",
  mba00f96381: "Retried on another route",
  mbccf9af6ee: "The connection may have dropped or something went wrong on our side. Your data and settings are safe.",
  mc5c38f461f: "All keys",
  mc7b62164df: "New key",
  mcde2cd071d: "Create",
  md32374b79b: "Search request ID, model or key",
  md44e9b3d3b: "Name",
  md5f5a7a010: "Today",
  mdb667b774d: "Load 60 more",
  mf203d577d1: "Unlimited",
  mf729bb3d3f: "Last 30 days",
};

/** @public loaded by regions.ts from the directory listing */
export default [
  ...parts.map((region) => ({ ...region, english })),
  ...["AxApp", "AxAppPhone"].flatMap((board) =>
    consoleParts.map((region) => ({
      ...region,
      board,
      english: consoleEnglish,
      render: { ...region.render, viewport: { width: board === "AxAppPhone" ? 390 : 1440, height: 900 } },
    })),
  ),
];
