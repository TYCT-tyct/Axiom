import type { Region } from "../regions.ts";

const english = {
  m99eb8c2896: "Polish the Chinese below. Keep the meaning and shorten the sentences.",
  md666f38b72: "Temperature 0.4, Max 2,000 tokens",
  mab5d9c5402: "Outline the topic in five sections with a sentence per section.",
  m1f87913ac5: "Temperature 0.6, Max 600 tokens",
  m3187029f2b: "Save a model, system prompt, and parameters under one name. Use ",
  m98472d23f3: "@preset/name",
  m181bcfdf83: " in code and update the request preset here.",
  m74ed19426d: "Use in code",
  mdfd719cbad: "Replace the model name with the request preset reference.",
  mf1de1e2621: "Request presets",
  ma61bc16ca0: "New request preset",
  m3fd47edce4: "Close",
  m6773310492: "Name",
  mc93ed832fd: "Name @preset/ Use lowercase letters, numbers, and hyphens.",
  mf4c4e8ad48: "System prompt",
  md6d8ddd71a: "Put this first in each request.",
  mffac84b31a: "Use the reference in code and update the request preset here.",
  m449597f5dd: "Press Enter to create",
  m2cd0f3be87: "Cancel",
  mcde2cd071d: "Create",
  mcbff1164e9: "Request presets couldn’t load",
  mbccf9af6ee: "Check your connection and try again.",
  mb8784c8dd5: "Retry",
};

function list(board: string): Region {
  return {
    board,
    name: "RequestPresets",
    english,
    page: "presets",
    roots: [".qpages:has(> .p-presets)"],
    omit: [".qpages > :not(.p-presets)", ".wv.w0", ".wv.w1", ".ppcnt"],
    repeats: { presets: ".wv.w2 .qpre" },
    text: { title: ".qptl h1", create: ".qpa button > span", example: ".qcode2 > span:nth-of-type(2)" },
    elements: {
      ".qpages": { set: { class: "{{loadingClass}}" } },
      ".p-presets": { set: { style: "{{layout}}" } },
      ".qptl h1": { set: { tabindex: "-1" } },
      ".qpa": { unless: "readOnly" },
      ".qpre": {
        set: {
          "data-slug": "",
          role: "{{cardRole}}",
          tabindex: "{{cardTab}}",
          onkeydown: "{{cardKey}}",
          style: "{{wrap}}",
        },
      },
      ".qhow": { unless: "noExample" },
    },
    classes: { loadingClass: ["pstL"] },
  };
}

const viewport = { width: 1440, height: 900 };
const root = ".nkdlg";
const field = ".nkf:has(.ppinw)";
const prompt = ".nkf:has(textarea)";
const editorScene: NonNullable<Region["render"]> = {
  root,
  viewport,
  actions: [["go", "presets"], { click: ".p-presets .qpa button" }],
};

// The board draws model chips from its demo catalogue and a few fixed numeric values.
// The API accepts arbitrary model names and values, so use the board's text field for those inputs.
const editor: Region = {
  board: "AxApp",
  name: "RequestPresetEditor",
  english,
  page: "presets",
  roots: [".fr"],
  render: {
    ...editorScene,
    fragment: {
      styles: {
        ".nkdlg": ["display", "flex-direction", "background-color", "border-radius", "box-shadow"],
        ".nkh h2": ["font-size", "line-height", "font-weight"],
        ".nkb textarea": ["height", "padding", "font-size", "line-height", "background-color", "border-radius"],
        ".nkfoot .pri": ["height", "background-color", "color", "border-radius"],
      },
    },
  },
  omit: [".nkmore", ".nkf:has(.nkchips)"],
  text: {
    title: ".nkh h2",
    remove: ".nkfl",
    save: ".nkfoot .pri",
    promptLabel: `${prompt} label`,
  },
  slots: { note: ".nkh p", inputs: field },
  elements: {
    ".fr": { set: { style: "display: contents;" } },
    [root]: {
      tag: "dialog",
      set: {
        "aria-labelledby": "{{titleId}}",
        ref: "{{dialogRef}}",
        style: "position: fixed; margin: 0; border: 0; padding: 0; color: inherit;",
      },
    },
    ".nkh h2": { set: { id: "{{titleId}}" } },
    ".nkx": { set: { onclick: "{{close}}", disabled: "{{busy}}" } },
    [prompt]: { unless: "hideFields" },
    [`${prompt} textarea`]: {
      set: { name: "system", placeholder: "", value: "{{system}}", readonly: "{{readOnly}}", disabled: "{{busy}}" },
    },
    ".nkfl": {
      tag: "button",
      set: { class: "btn sec dan2 nkfl", type: "button", onclick: "{{deletePreset}}", disabled: "{{busy}}" },
      unless: "hideDelete",
    },
    ".nkfoot > [data-a=x]": { set: { onclick: "{{close}}", disabled: "{{busy}}" } },
    ".nkfoot .pri": { set: { type: "submit", disabled: "{{busy}}" }, unless: "readOnly" },
  },
};

const input: Region = {
  board: "AxApp",
  name: "RequestPresetInput",
  english,
  page: "presets",
  roots: [field],
  render: {
    ...editorScene,
    fragment: {
      styles: {
        ".nkf:has(.ppinw) .nkl": ["display", "font-size", "font-weight", "color", "margin-bottom"],
        ".ppinw": ["display", "height", "border-radius", "background-color"],
        // The board reduces the left padding while its @preset/ prefix is present.
        ".ppinw input": ["font-family", "font-size", "color", "padding-right"],
      },
    },
  },
  omit: [".ppinw > span", ".nkhint"],
  text: { label: "label" },
  elements: {
    label: { set: { for: "{{id}}" } },
    input: {
      set: {
        id: "{{id}}",
        name: "{{name}}",
        value: "{{initial}}",
        placeholder: "",
        readonly: "{{readOnly}}",
        disabled: "{{busy}}",
      },
    },
  },
};

const stateRoot = ".stb.err";
const state: Region = {
  board: "AxApp",
  name: "RequestPresetsState",
  english,
  page: "presets",
  roots: [stateRoot],
  render: {
    root: stateRoot,
    viewport,
    actions: [["go", "presets"]],
    props: { view: "出错" },
    fragment: {
      values: { tone: "err", stateRole: "alert" },
      styles: {
        ".stb.err": ["display", "flex-direction", "align-items", "padding"],
        ".stb.err .stbi": ["display", "width", "height", "color", "background-color", "border-radius"],
        ".stb.err h2": ["font-size", "line-height", "font-weight", "color"],
        ".stb.err [data-st=retry]": ["height", "background-color", "color", "border-radius"],
      },
    },
  },
  omit: ['[data-st="report"]', ".stbid"],
  text: { title: "h2", detail: "p", action: "button > span" },
  elements: {
    [stateRoot]: { set: { class: "stb {{tone}}", role: "{{stateRole}}" } },
    ".stba": { unless: "noAction" },
    button: { set: { onclick: "{{retry}}" } },
  },
  classes: { tone: ["empty", "err", "deny"] },
};

/** @public loaded by regions.ts from the directory listing */
export default ["AxApp", "AxAppPhone"].map(list).concat(editor, input, state);
