import type { Region } from "../regions.ts";

type Part = Omit<Region, "board">;
type Scene = NonNullable<Region["render"]>;
const h = (name: string) => `{{${name}}}`;
const newKey: Scene = {
  root: ".nkdlg",
  actions: [
    ["go", "keys"],
    ["nkx", {}],
  ],
};
const keyResult: Scene = { ...newKey, actions: [...newKey.actions, { click: '.nkdlg [data-a="ok"]' }] };
const advancedKey: Scene = { ...newKey, actions: [...newKey.actions, { click: ".nkdlg .nkmh" }] };
const field = ".nkdlg .nkb > .nkf:first-child";
const checkbox = '.nkdlg .nkrow:has([data-a="ip"])';
const code = ".p-plugins pre.qcode2.one";
const meter = ".p-bill .sxP .qrun .rwb";

function button(name: string, root: string, kind: string, page: string, render: Scene): Part {
  return {
    name,
    roots: [root],
    page,
    render,
    slots: { label: root },
    elements: {
      [root]: {
        set: {
          class: `${kind} {{disabledClass}}`,
          disabled: h("disabled"),
          ref: h("button"),
          onclick: h("press"),
        },
      },
    },
    classes: { disabledClass: ["btn", "dis"] },
  };
}

function copy(name: string, copied: boolean): Part {
  const root = '.nkkey > [data-a="copy"]';
  return {
    name,
    roots: [root],
    page: "keys",
    render: copied ? { ...keyResult, actions: [...keyResult.actions, { click: root }] } : keyResult,
    slots: { label: root },
    elements: {
      [root]: { set: { class: "btn {{tone}} sm", onclick: h("copy"), disabled: h("busy"), ref: h("button") } },
    },
    classes: { tone: ["pri", "sec", "dis"] },
  };
}

const feedback = {
  set: { class: h("feedbackClass"), id: h("feedbackId") },
  unless: "hideFeedback",
};

const parts: Part[] = [
  button("AdminPrimaryButton", ".nkfoot .pri", "btn pri", "keys", newKey),
  button("AdminSecondaryButton", ".nkfoot .sec", "btn sec", "keys", newKey),
  button("AdminDangerButton", ".nkfoot .dan2", "btn sec dan2", "security", {
    root: ".nkdlg",
    actions: [
      ["go", "security"],
      ["ppSesAll", {}],
    ],
  }),
  button("AdminLinkButton", ".p-ov .wv.w2 .qlnk[data-pg=logs]", "qlnk", "ov", {
    root: ".p-ov",
    actions: [["go", "ov"]],
  }),
  {
    name: "AdminSelectField",
    roots: [field],
    page: "keys",
    render: newKey,
    slots: { options: "select" },
    text: { label: "label", feedback: ".nkf > div:last-child" },
    elements: {
      label: { set: { for: h("id") } },
      input: {
        tag: "select",
        set: {
          class: "nkin {{invalidClass}}",
          id: h("id"),
          name: h("name"),
          value: h("initial"),
          "aria-describedby": h("describedBy"),
          disabled: h("disabled"),
          ref: h("control"),
          oninput: h("input"),
        },
      },
      ".nkhint": feedback,
    },
    classes: { invalidClass: ["bad"], feedbackClass: ["nkhint", "nkerr"] },
  },
  {
    name: "AdminCheckbox",
    roots: [checkbox],
    page: "keys",
    render: advancedKey,
    text: { label: ".nkl" },
    elements: {
      ".nkl": { tag: "label", set: { for: h("id") } },
      button: {
        set: {
          class: "sw2 qsw {{checkedClass}}",
          role: "checkbox",
          id: h("id"),
          "aria-label": h("label"),
          "aria-checked": h("checked"),
          "aria-describedby": h("describedBy"),
          disabled: h("disabled"),
          onclick: h("toggle"),
        },
      },
    },
    classes: { checkedClass: ["on", "pplock"] },
  },
  {
    name: "AdminFieldFrame",
    roots: [field],
    page: "keys",
    render: newKey,
    children: field,
  },
  {
    name: "AdminFieldFeedback",
    roots: [`${field} > .nkhint`],
    page: "keys",
    render: newKey,
    text: { feedback: "div" },
    elements: { ".nkhint": feedback },
    classes: { feedbackClass: ["nkhint", "nkerr"] },
  },
  {
    name: "AdminCodeBlock",
    roots: [code],
    page: "plugins",
    render: { root: ".p-plugins", actions: [["go", "plugins"]] },
    children: code,
    elements: {
      [code]: { set: { role: "region", "aria-label": h("label"), tabindex: "0" } },
    },
  },
  {
    name: "AdminCodeViewport",
    roots: [".lgd"],
    page: "logs",
    render: { root: ".fr", actions: [["go", "logs"], { click: ".p-logs .wv.w2 .lgr" }] },
    children: ".lgd",
    elements: { ".lgd": { set: { class: "lgd", tabindex: "-1" } } },
  },
  {
    name: "AdminCodeToken",
    roots: [`${code} > .c2:first-of-type`],
    text: { token: ".c2" },
  },
  copy("AdminCopyButton", false),
  copy("AdminCopiedButton", true),
  {
    name: "AdminElapsedTrack",
    roots: [meter],
    page: "bill",
    render: { root: ".p-bill", actions: [["go", "bill"]] },
    omit: ["em"],
    elements: {
      [meter]: { set: { role: "meter", "aria-label": h("label"), ref: h("meter") } },
      i: { set: { style: h("fill") } },
    },
  },
];

const english = {
  mcde2cd071d: "Create",
  m2cd0f3be87: "Cancel",
  m949c8b537e: "Sign out all other devices",
  m27975a68f2: "All logs",
  md44e9b3d3b: "Name",
  m0cdc278f81: "Use a name you will recognize in logs and usage, such as a service name.",
  m472e9ec385: "Only allow these IP addresses",
  m63d90d9773: "Copy",
  m8f6f8d979c: "Copied",
};

/** @public loaded by regions.ts from the directory listing */
export default ["AxApp", "AxAppPhone"].flatMap((board) =>
  parts.map(
    (part): Region => ({
      ...part,
      board,
      english,
      ...(part.render && {
        render: {
          ...part.render,
          viewport: { width: board === "AxAppPhone" ? 390 : 1440, height: 900 },
        },
      }),
    }),
  ),
);
