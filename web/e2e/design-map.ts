import type { Page } from "@playwright/test";
import { requestPresetText } from "../console/src/locales/request-presets.ts";
import presetsEn from "../packages/boards/src/AxApp/regions/RequestPresets.en.ts";
import presetsZh from "../packages/boards/src/AxApp/regions/RequestPresets.zh.ts";
import shellEn from "../packages/boards/src/AxApp/regions/Shell.en.ts";
import shellZh from "../packages/boards/src/AxApp/regions/Shell.zh.ts";
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
  language?: "en";
  localizeReference?: (page: Page) => Promise<void>;
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

const presetScenes: DesignScene[] = (["zh", "en"] as const).map((language) => ({
  name: language === "en" ? "console request-presets en" : "console request-presets",
  suite: consoleSuite,
  scene: "request-presets",
  fonts: "console",
  boards: consoleBoards("presets"),
  schemes: consoleSchemes,
  language: language === "en" ? "en" : undefined,
  localizeReference: async (page) => {
    const labels = requestPresetText(language);
    const localized: Record<string, string> = {
      [presetsZh.mf1de1e2621]: labels.title,
      [presetsZh.ma61bc16ca0]: labels.create,
      [presetsZh.m181bcfdf83]: labels.introEnd,
      [presetsZh.mdfd719cbad]: labels.exampleHint,
    };
    if (language === "en") {
      for (const key of Object.keys(shellEn) as (keyof typeof shellEn)[]) localized[shellZh[key]] = shellEn[key];
      localized[shellZh.mf1de1e2621] = labels.title;
      localized[shellZh.mb9670c85a4] = "Routing presets";
      // The prompts are API fixtures. Translate the UI surrounding them, including its parameter labels.
      const keys = ["m3187029f2b", "m98472d23f3", "m74ed19426d", "md666f38b72", "m1f87913ac5"] as const;
      for (const key of keys) localized[presetsZh[key]] = presetsEn[key];
    }
    await page.evaluate((strings) => {
      const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
      for (let node = walker.nextNode(); node; node = walker.nextNode()) {
        const translation = strings[node.textContent ?? ""];
        if (translation !== undefined) node.textContent = translation;
      }
    }, localized);
  },
}));

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
    name: "console keys",
    suite: consoleSuite,
    scene: "keys",
    fonts: "console",
    boards: consoleBoards("keys"),
    schemes: consoleSchemes,
  },
  ...presetScenes,
  {
    name: "site home",
    suite: siteSuite,
    scene: "home",
    fonts: "site/home",
    boards: siteBoards,
    schemes: { light: { system: "浅色" }, dark: { system: "深色" } },
  },
];
