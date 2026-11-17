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

export const designScenes: DesignScene[] = [
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
  {
    name: "site home",
    suite: siteSuite,
    scene: "home",
    fonts: "site/home",
    boards: siteBoards,
    schemes: { light: { system: "浅色" }, dark: { system: "深色" } },
  },
];
