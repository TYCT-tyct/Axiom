import { readFileSync } from "node:fs";
import { expect, type Page } from "@playwright/test";
import { en as consoleAuthEn, zh as consoleAuthZh } from "../console/src/locales/auth.ts";
import consoleEn from "../console/src/locales/en.ts";
import consoleZh from "../console/src/locales/zh.ts";
import presetsEn from "../packages/boards/src/AxApp/regions/RequestPresets.en.ts";
import presetsZh from "../packages/boards/src/AxApp/regions/RequestPresets.zh.ts";
import shellEn from "../packages/boards/src/AxApp/regions/Shell.en.ts";
import shellZh from "../packages/boards/src/AxApp/regions/Shell.zh.ts";
import uiEn from "../packages/ui/src/locales/en.ts";
import uiZh from "../packages/ui/src/locales/zh.ts";
import { consoleSuite, requestPresetPrompts } from "./console-scenes.ts";
import type { BoardScene } from "./design/board.ts";
import type { Suite } from "./scene.ts";
import { siteSuite } from "./site-scenes.ts";

// Which design board each end-to-end scene is compared with (design.spec.ts). A design scene names a suite's scene
// (console-scenes.ts, admin-scenes.ts) and, per viewport width, the board in AXIOM_DESIGN_DIR and the props and actions
// that show the same screen; the colour scheme is a prop of its own. A later screen adds an entry here, and its
// product rulings in design/rulings.ts.
export type DesignScene = {
  name: string;
  suite: Suite;
  scene: string;
  // The page of fonts.json the build's page belongs to: the faces it must have loaded.
  fonts: string | string[];
  boards: Record<number, BoardScene & { height: number }>;
  schemes: Record<"light" | "dark", Record<string, string>>;
  languages?: ("zh" | "en")[];
  prepareComparison?: (board: Page, page: Page, language: "zh" | "en") => Promise<void>;
  textRasterization?: "grayscale" | "unhinted";
};

// The boards' own "today": the console boards date their demo data around 2026-10-24 (the top-up board's pyDate).
export const demoTime = new Date("2026-10-24T10:00:00+08:00");

// The console boards come in one Component per form factor; theme is a prop, and the personal space is the default.
const consoleSchemes = { light: { theme: "浅色" }, dark: { theme: "深色" } };
const consoleBoards = (page: string) => ({
  1440: { file: "AxApp.dc.html", height: 900, actions: [["go", page]] as [string, string][] },
  390: { file: "AxAppPhone.dc.html", height: 844, actions: [["go", page]] as [string, string][] },
});

// The console's sign-in pages come from the Login boards: one per form factor, the scheme a prop (mode). The form is
// drawn by login-ui.js, so the reference runs it, and the sea's engine, whose opaque canvas covers the sea's edge as the
// page's does; both seas are blanked (rulings.ts).
const loginBoards = {
  1440: { file: "Login.dc.html", height: 900, engines: ["ocean-orig", "login-ui"] },
  390: { file: "LoginPhone.dc.html", height: 844, engines: ["ocean-orig", "login-ui"] },
};

// The sign-in page asks what the product asks, in the board's parts: its title and lede, the pitch's billing line
// (product.md: billed by the endpoint that served, without the board's promise that every route is tested) and its
// promise (the models the account can use), and the way
// to register in a note, where the board's email step has the sign-up-on-the-way copy, the terms' box and the providers the mocked site
// does not offer (rulings.ts lists the parts of the board's flow left out). Put the page's words in the reference and
// take out what the page has no counterpart for, so the comparison measures the parts and their layout.
async function prepareSignInComparison(board: Page, page: Page, language: "zh" | "en") {
  // The page edges its controls in the console's control edge (docs/frontend.md「对比度令牌」), where the board draws a
  // lighter line: the reference takes the page's resolved colour, so the controls' place and fill stay compared.
  const edge = await page.evaluate(() => {
    const probe = document.createElement("i");
    probe.style.color = "var(--ctl-line)";
    (document.querySelector(".lg") as HTMLElement).append(probe);
    const resolved = getComputedStyle(probe).color;
    probe.remove();
    return resolved;
  });
  await board.evaluate((edge) => {
    const lg = document.querySelector(".lg") as HTMLElement;
    for (const name of ["--lineStrong1x", "--lineStrong2x"]) lg.style.setProperty(name, edge);
    lg.style.setProperty("--bw", "1px");
  }, edge);
  const auth = language === "en" ? consoleAuthEn : consoleAuthZh;
  const words = {
    title: (language === "en" ? uiEn : uiZh).auth.signIn.title,
    lede: auth.pitch.lede,
    tail: auth.pitch.tail,
    ...auth.signIn,
  };
  await board.evaluate((words) => {
    const step = document.querySelector(".lg-step") as HTMLElement;
    // the words in their own inline boxes, as the page sets them, so only the glyphs are excused, not where lines sit
    const inline = (text: string) => Object.assign(document.createElement("span"), { textContent: text });
    (step.querySelector(".lg-title") as HTMLElement).textContent = words.title;
    (step.querySelector(".lg-sub") as HTMLElement).replaceChildren(inline(words.description));
    (document.querySelector(".lg-pitch .s") as HTMLElement).replaceChildren(inline(words.lede));
    // the pitch's promise as the product makes it (the models the account can use, not every model)
    const pitch = document.querySelector(".lg-pitch .h") as HTMLElement;
    (pitch.lastChild as Text).textContent = words.tail;
    for (const part of step.querySelectorAll(".lg-consent, .lg-or, .lg-stack")) part.remove();
    const note = document.createElement("p");
    note.className = "lg-note";
    const link = document.createElement("a");
    link.href = "#";
    link.textContent = words.register;
    note.append(inline(words.noAccount), " ", link);
    step.append(note);
    // the page sets a button's label in a span of its own, which the rulings excuse instead of the whole button
    for (const button of document.querySelectorAll(".lg-btn")) {
      const label = document.createElement("span");
      label.append(...button.childNodes);
      button.append(label);
    }
  }, words);
  // the page's lede is the region's bare text: set in an inline box too, with the same words, so both sides excuse
  // only its glyphs
  await page.evaluate(() => {
    const lede = document.querySelector(".lg-pitch .s") as HTMLElement;
    lede.replaceChildren(Object.assign(document.createElement("span"), { textContent: lede.textContent }));
  });
}

// The website's home boards come one per width; the scheme is the system's, a prop.
const siteBoards = {
  1440: { file: "Site.dc.html", height: 900 },
  820: { file: "SiteTablet.dc.html", height: 1180 },
  390: { file: "SitePhone.dc.html", height: 844 },
};

type Face = { family: string; weight: string; axes?: { wght?: number | number[] } };
const { faces }: { faces: Face[] } = JSON.parse(
  readFileSync(new URL("../scripts/fonts/sources.json", import.meta.url), "utf8"),
);
const referenceFonts = [
  ["Axiom Sans", "Axiom Sans"],
  ["Axiom Mono", "Axiom Mono"],
  ["Axiom CJK", "Axiom CJK Console"],
].map(([boardFamily, shippedFamily]) => [boardFamily, faces.find(({ family }) => family === shippedFamily)] as const);

const presetScenes: DesignScene[] = (["zh", "en"] as const).map((language) => ({
  name: language === "en" ? "console request-presets en" : "console request-presets",
  suite: consoleSuite,
  scene: "request-presets",
  fonts: "console/pages/request-presets",
  boards: consoleBoards("presets"),
  schemes: consoleSchemes,
  languages: [language],
  textRasterization: language === "zh" ? "unhinted" : "grayscale",
  prepareComparison: async (board) => {
    const messages = language === "en" ? consoleEn : consoleZh;
    const labels = messages.requestPresets.copy;
    const localized: Record<string, string> = {
      [presetsZh.mf1de1e2621]: labels.title,
      [presetsZh.ma61bc16ca0]: labels.create,
      [presetsZh.m181bcfdf83]: labels.introEnd,
      [presetsZh.mdfd719cbad]: labels.exampleHint,
      [presetsZh.m99eb8c2896]: requestPresetPrompts[0],
      [presetsZh.mab5d9c5402]: requestPresetPrompts[1],
    };
    if (language === "en") {
      for (const key of Object.keys(shellEn) as (keyof typeof shellEn)[]) localized[shellZh[key]] = shellEn[key];
      localized[shellZh.mf1de1e2621] = labels.title;
      localized[shellZh.mb9670c85a4] = messages.nav.routingPresets;
      // The prompts are API fixtures. Translate the UI surrounding them, including its parameter labels.
      const keys = ["m3187029f2b", "m98472d23f3", "m74ed19426d", "md666f38b72", "m1f87913ac5"] as const;
      for (const key of keys) localized[presetsZh[key]] = presetsEn[key];
    }
    await board.evaluate((strings) => {
      const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
      for (let node = walker.nextNode(); node; node = walker.nextNode()) {
        const translation = strings[node.textContent ?? ""];
        if (translation !== undefined) node.textContent = translation;
      }
    }, localized);
    await board.evaluate(useProductFontWeights, Object.fromEntries(referenceFonts));
    // The console's base typography balances headings and avoids orphaned words.
    await board.addStyleTag({
      content:
        ".p-presets :is(h1,h2){text-wrap:balance;word-break:keep-all;overflow-wrap:anywhere}.p-presets p{text-wrap:pretty}",
    });
    await board.evaluate(() => document.fonts.ready.then(() => {}));
  },
}));

// Keep the uncut source fonts, using the console's face descriptors and pinned weight axes.
function useProductFontWeights(faces: Record<string, Face | undefined>) {
  for (const rule of [...document.styleSheets].flatMap((sheet) => [...sheet.cssRules])) {
    if (!(rule instanceof CSSFontFaceRule)) continue;
    const family = rule.style.getPropertyValue("font-family").replaceAll(/["']/g, "");
    const face = faces[family];
    if (!face) continue;
    rule.style.setProperty("font-weight", face.weight);
    const axis = face.axes?.wght;
    if (typeof axis === "number") rule.style.setProperty("font-variation-settings", `"wght" ${axis}`);
  }
}

async function prepareModelsComparison(board: Page, page: Page, language: "zh" | "en") {
  // Put the real page copy in the reference's title and lede so the comparison sees their typography and layout.
  const { title, description } = (language === "en" ? uiEn : uiZh).publicModels;
  await board.locator(".hl > h2").evaluate((el, title) => {
    el.textContent = title;
  }, title);
  await board.locator(".hl > .lede").evaluate((el, description) => {
    el.textContent = description;
  }, description);
  await board.evaluate((language) => {
    document.documentElement.lang = language === "en" ? "en-US" : "zh-CN";
    for (const el of document.querySelectorAll(".layer, .stats, .leads, .pick")) el.remove();
  }, language);
  // The shell uses slate2 in light and slate1 in dark. Removing the lens also removes the sea's grayscale text layer.
  // The deployed MiSans body face is instanced at 330, while the reference serves the source variable font.
  await board.addStyleTag({
    content: `
      @media (prefers-color-scheme: light) { .board { --bg: var(--panel) !important; } }
      .mx .lens::before { display: none; }
      .mx .lede { font-weight: 330; }
      @media (width: 390px) { .sec { margin-top: 16px; } }
    `,
  });
  // Compare the outlines of the source and instanced MiSans without variable-font hinting moving individual strokes.
  if (language === "zh") {
    for (const side of [board, page]) {
      await side.locator(".hl > .lede").evaluate((el: HTMLElement) => {
        el.style.textRendering = "geometricPrecision";
      });
    }
  }
  await board.evaluate(() => document.fonts.ready.then(() => {}));

  // The frame is transparent, so check its spacing even where the screenshot masks the tabs and form fields.
  const frame = (side: Page) =>
    side.locator(".tabs.flt").evaluate((el) => {
      const style = getComputedStyle(el);
      const box = el.getBoundingClientRect();
      const lede = document.querySelector(".hl > .lede")?.getBoundingClientRect();
      return {
        x: box.x,
        ledeGap: lede && box.y - lede.bottom,
        display: style.display,
        gap: style.gap,
        padding: style.padding,
        margin: style.margin,
        shadow: style.boxShadow,
      };
    });
  expect(await frame(page)).toEqual(await frame(board));
}

export const designScenes: DesignScene[] = [
  // The shell on its own: the page inside it is ruled out here and compared in the page's scene. 820px is the tablet
  // board, the wide board's markup with its sidebar folded.
  {
    name: "console shell",
    suite: consoleSuite,
    scene: "overview",
    fonts: "console",
    boards: {
      ...consoleBoards("ov"),
      820: { file: "AxAppTablet.dc.html", height: 1180, actions: [["go", "ov"]] },
    },
    schemes: consoleSchemes,
  },
  {
    name: "console overview",
    suite: consoleSuite,
    scene: "overview",
    fonts: "console",
    boards: consoleBoards("ov"),
    schemes: consoleSchemes,
  },
  {
    name: "console usage",
    suite: consoleSuite,
    scene: "usage",
    fonts: "console",
    boards: consoleBoards("usage"),
    schemes: consoleSchemes,
  },
  {
    name: "console route",
    suite: consoleSuite,
    scene: "routing-presets",
    fonts: "console",
    boards: consoleBoards("route"),
    schemes: consoleSchemes,
  },
  {
    name: "console byok",
    suite: consoleSuite,
    scene: "provider-keys",
    fonts: "console",
    boards: consoleBoards("byok"),
    schemes: consoleSchemes,
  },
  {
    name: "console keys",
    suite: consoleSuite,
    scene: "keys",
    fonts: "console",
    boards: consoleBoards("keys"),
    schemes: consoleSchemes,
  },
  ...presetScenes,
  {
    name: "console bill",
    suite: consoleSuite,
    scene: "balance",
    fonts: "console",
    boards: consoleBoards("bill"),
    schemes: consoleSchemes,
  },
  {
    name: "console calls",
    suite: consoleSuite,
    scene: "calls",
    fonts: "console",
    boards: consoleBoards("logs"),
    schemes: consoleSchemes,
  },
  {
    name: "console settings",
    suite: consoleSuite,
    scene: "settings",
    fonts: "console",
    boards: consoleBoards("profile"),
    schemes: consoleSchemes,
  },
  {
    name: "console security",
    suite: consoleSuite,
    scene: "security",
    fonts: "console",
    boards: consoleBoards("security"),
    schemes: consoleSchemes,
  },
  {
    name: "console notifications",
    suite: consoleSuite,
    scene: "notifications",
    fonts: "console",
    boards: consoleBoards("notify"),
    schemes: consoleSchemes,
  },
  {
    name: "console sign-in",
    suite: consoleSuite,
    scene: "sign-in",
    // the console's Latin faces, and the sign-in route's own: the Chinese and the serif the Login board names
    fonts: ["console", "console/auth/sign-in"],
    boards: loginBoards,
    schemes: { light: { mode: "浅色" }, dark: { mode: "深色" } },
    prepareComparison: prepareSignInComparison,
  },
  {
    name: "site home",
    suite: siteSuite,
    scene: "home",
    fonts: "site/home",
    boards: siteBoards,
    schemes: { light: { system: "浅色" }, dark: { system: "深色" } },
  },
  {
    name: "site models",
    suite: siteSuite,
    scene: "models",
    fonts: "site/models",
    boards: {
      1440: { file: "Models.dc.html", height: 900 },
      390: { file: "ModelsPhone.dc.html", height: 844 },
    },
    schemes: consoleSchemes,
    languages: ["zh", "en"],
    prepareComparison: prepareModelsComparison,
  },
];
