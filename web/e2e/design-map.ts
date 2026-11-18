import { expect, type Page } from "@playwright/test";
import en from "../packages/ui/src/locales/en.ts";
import zh from "../packages/ui/src/locales/zh.ts";
import { consoleSuite } from "./console-scenes.ts";
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
  fonts: string;
  boards: Record<number, BoardScene & { height: number }>;
  schemes: Record<"light" | "dark", Record<string, string>>;
  languages?: ("zh" | "en")[];
  prepareComparison?: (board: Page, page: Page, language: "zh" | "en") => Promise<void>;
};

// The boards' own "today": the console boards date their demo data around 2026-10-24 (the top-up board's pyDate).
export const demoTime = new Date("2026-10-24T10:00:00+08:00");

// The console boards come in one Component per form factor; theme is a prop, and the personal space is the default.
const consoleSchemes = { light: { theme: "浅色" }, dark: { theme: "深色" } };
const consoleBoards = (page: string) => ({
  1440: { file: "AxApp.dc.html", height: 900, actions: [["go", page]] as [string, string][] },
  390: { file: "AxAppPhone.dc.html", height: 844, actions: [["go", page]] as [string, string][] },
});

// The website's home boards come one per width; the scheme is the system's, a prop.
const siteBoards = {
  1440: { file: "Site.dc.html", height: 900 },
  820: { file: "SiteTablet.dc.html", height: 1180 },
  390: { file: "SitePhone.dc.html", height: 844 },
};

async function prepareModelsComparison(board: Page, page: Page, language: "zh" | "en") {
  // Put the real page copy in the reference's title and lede so the comparison sees their typography and layout.
  const { title, description } = (language === "en" ? en : zh).publicModels;
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
  {
    name: "console calls",
    suite: consoleSuite,
    scene: "calls",
    fonts: "console",
    boards: consoleBoards("logs"),
    schemes: consoleSchemes,
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
