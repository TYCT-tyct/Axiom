import { readdirSync, readFileSync } from "node:fs";
import { expect, type Page } from "@playwright/test";
import consoleEn from "../console/src/locales/en.ts";
import consoleZh from "../console/src/locales/zh.ts";
import presetsEn from "../packages/boards/src/AxApp/regions/RequestPresets.en.ts";
import presetsZh from "../packages/boards/src/AxApp/regions/RequestPresets.zh.ts";
import shellEn from "../packages/boards/src/AxApp/regions/Shell.en.ts";
import shellZh from "../packages/boards/src/AxApp/regions/Shell.zh.ts";
import { en as consoleAuthEn, zh as consoleAuthZh } from "../packages/login/src/locales/auth.ts";
import uiEn from "../packages/ui/src/locales/en.ts";
import uiZh from "../packages/ui/src/locales/zh.ts";
import { withLastMonth } from "./console-calls-data.ts";
import type { MockState } from "./console-data.ts";
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
  // The scene whose rulings apply, when this one is another language of it (rulings.ts names scenes, not languages).
  rulingsOf?: string;
  // What the scene's mocked API state is changed to, before the page opens, so it holds the board's demo terms.
  account?: (state: unknown) => void;
  prepareComparison?: (board: Page, page: Page, language: "zh" | "en") => Promise<void>;
  textRasterization?: "grayscale" | "unhinted";
};

// The boards' own "today": the console boards date their demo data around 2026-10-24 (the top-up board's pyDate).
export const demoTime = new Date("2026-10-24T10:00:00+08:00");

// The console boards come in one Component per form factor; theme is a prop, and the personal space is the default.
const consoleSchemes = { light: { theme: "浅色" }, dark: { theme: "深色" } };
// The tablet board is the wide board's markup with its sidebar folded; it opens in the team space, so it asks for the
// personal one the other widths open in.
const consoleBoards = (page: string) => ({
  1440: { file: "AxApp.dc.html", height: 900, actions: [["go", page]] as [string, string][] },
  820: {
    file: "AxAppTablet.dc.html",
    height: 1180,
    props: { space: "个人" },
    actions: [["go", page]] as [string, string][],
  },
  390: { file: "AxAppPhone.dc.html", height: 844, actions: [["go", page]] as [string, string][] },
});

// The console's sign-in pages come from the Login boards: one per form factor, the scheme a prop (mode). The form is
// drawn by login-ui.js, so the reference runs it, and the sea's engine, whose opaque canvas covers the sea's edge as the
// page's does; both seas are blanked (rulings.ts).
const loginBoards = {
  1440: { file: "Login.dc.html", height: 900, engines: ["ocean-orig", "login-ui"] },
  390: { file: "LoginPhone.dc.html", height: 844, engines: ["ocean-orig", "login-ui"] },
};

// A sign-in page's email step asks what the product asks, in the board's parts: its title and lede, the pitch's billing
// line (product.md: billed by the endpoint that served, without the board's promise that every route is tested) and its
// promise (the models the account can use), and its way on in a note, where the board's email step has the
// sign-up-on-the-way copy, the terms' box and the providers the mocked site does not offer (rulings.ts lists the parts
// of the board's flow left out). The sign-in page's way on is to register; open registration's, to sign in; the reset
// page's, the request for a reset
// link, is back to sign in. Put the page's words in the reference and take out what the page has no counterpart for,
// so the comparison measures the parts and their layout.
type EmailStepWords = { title: string; description: string; submit: string; lead?: string; link: string };

const signInWords = (language: "zh" | "en"): EmailStepWords => {
  const auth = language === "en" ? consoleAuthEn : consoleAuthZh;
  return {
    title: (language === "en" ? uiEn : uiZh).auth.signIn.title,
    description: auth.signIn.description,
    submit: auth.signIn.next,
    lead: auth.signIn.noAccount,
    link: auth.signIn.register,
  };
};

const registerWords = (language: "zh" | "en"): EmailStepWords => {
  const { register } = language === "en" ? consoleAuthEn : consoleAuthZh;
  return {
    title: register.title,
    description: register.openDescription,
    submit: register.submitOpen,
    lead: register.haveAccount,
    link: register.signIn,
  };
};

const resetWords = (language: "zh" | "en"): EmailStepWords => {
  const { reset } = language === "en" ? consoleAuthEn : consoleAuthZh;
  return { title: reset.title, description: reset.description, submit: reset.submit, link: reset.back };
};

async function prepareEmailStep(board: Page, page: Page, language: "zh" | "en", own: EmailStepWords) {
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
  // The page's base typography balances headings and keeps a paragraph's last line from standing alone, as the request
  // presets' scene gives the board too: the words are the page's, so they break where the page breaks them.
  await board.addStyleTag({
    content: ".lg :is(h1,h2){text-wrap:balance;word-break:keep-all;overflow-wrap:anywhere}.lg p{text-wrap:pretty}",
  });
  if (language === "en") {
    await translateBoard(board, loginTranslations);
    // the board's pitch kerns a Chinese comma after "key"; the English pitch has none
    await board.evaluate(() => document.querySelector(".lg-pitch .kern")?.remove());
  }
  const { pitch } = language === "en" ? consoleAuthEn : consoleAuthZh;
  const words = { ...own, lede: pitch.lede, tail: pitch.tail };
  await board.evaluate((words) => {
    const step = document.querySelector(".lg-step") as HTMLElement;
    // the words in their own inline boxes, as the page sets them, so only the glyphs are excused, not where lines sit
    const inline = (text: string) => Object.assign(document.createElement("span"), { textContent: text });
    // the title a character to a box, as the page sets it for its decode, so each glyph sits where the page's does
    (step.querySelector(".lg-title") as HTMLElement).replaceChildren(...[...words.title].map(inline));
    (step.querySelector(".lg-sub") as HTMLElement).replaceChildren(inline(words.description));
    (document.querySelector(".lg-pitch .s") as HTMLElement).replaceChildren(inline(words.lede));
    // the pitch's promise as the product makes it (the models the account can use, not every model)
    const pitch = document.querySelector(".lg-pitch .h") as HTMLElement;
    (pitch.lastChild as Text).textContent = words.tail;
    for (const part of step.querySelectorAll(".lg-consent, .lg-or, .lg-stack")) part.remove();
    (step.querySelector(".lg-btn.primary") as HTMLElement).textContent = words.submit;
    const note = document.createElement("p");
    note.className = "lg-note";
    const link = document.createElement("a");
    link.href = "#";
    link.textContent = words.link;
    if (words.lead) note.append(inline(words.lead), " ");
    note.append(link);
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

// The website's home boards come one per width; the scheme is the system's, a prop. They are compared in Chinese only:
// the boards' script composes the headings character by character, which no map of the regions' text puts into English.
const siteBoards = {
  1440: { file: "Site.dc.html", height: 900 },
  820: { file: "SiteTablet.dc.html", height: 1180 },
  390: { file: "SitePhone.dc.html", height: 844 },
};

// What the page writes in its own words, by scene (packages/boards/manifest/site-screens-b.ts and -c.ts, the regions' text).
const pageWords: Record<string, string[]> = {
  "site connect": [".hfm1 h2 > .ph:last-child", ".hfm1 .intro p"],
  "site faq": [
    ".hfm5 .ans",
    ".hfm5 .w7 .kv:last-child > .t1",
    ".hfm5 .w7 .kv:first-child > .t1",
    ".hfm5 .w2 .cl:first-child > .cm",
  ],
  "site bed": [".hfm6 .el > .lede", ".hfm6 .cta .btn.primary", ".hfm6 .ft .fb > p"],
};

// A screen of the home page below the first: both sides are scrolled until the screen's section is at the top of the
// scroller, the page's code having come and the screen faded in (the board's own IO, which the harness leaves out, adds
// its class). The board's words are Chinese only, so these scenes are compared in Chinese.
const screenAt = (sec: number, scene: string) => async (board: Page, page: Page) => {
  // the board's own script sets the frame's width for its stylesheet and marks the tablet layout (--fw, data-tab); the
  // harness leaves the script's engines out, so the shot does what the script does
  await board.locator(".tr.site").evaluate((root) => {
    const ho = root.querySelector(".ho") as HTMLElement;
    ho.style.setProperty("--fw", `${root.clientWidth}px`);
    ho.toggleAttribute("data-tab", root.classList.contains("m") && root.clientWidth >= 700);
  });
  await board.locator(`.hfm${sec}`).evaluate((el) => {
    el.classList.add("in");
    el.scrollIntoView({ block: "start" });
  });
  const place = page.locator(`.under [data-place="${sec}"]`);
  await place.evaluate((el) => el.scrollIntoView({ block: "start" }));
  await expect.poll(() => place.locator(".hfm").evaluate((el) => getComputedStyle(el).opacity)).toBe("1");
  // the board shows only this screen, the others not having come in; the page's neighbours that have are put back
  await page.locator(`.under [data-place]:not([data-place="${sec}"]) .hfm`).evaluateAll((els) => {
    for (const el of els) (el as HTMLElement).style.setProperty("opacity", "0");
  });
  // the deployed MiSans body face is instanced at 330, the board's is the source variable font: compare the outlines, without
  // variable-font hinting moving single strokes (as the models page's scene does)
  for (const side of [board, page]) {
    await side
      .locator(".under")
      .evaluate((el) => el.style.setProperty("text-rendering", "geometricPrecision", "important"));
    await side.evaluate(() => document.fonts.ready.then(() => {}));
  }
  // The page's own words where the board's would not hold for every site (the PR's list of sentences against the docs:
  // the answers, the billing line, the connect heading's promise and lede, the sea bed's way in and its lines): the
  // board's get the page's text, so the layout under them is compared.
  for (const selector of pageWords[scene] ?? []) {
    const said = await page.locator(selector).allTextContents();
    await board.locator(selector).evaluateAll((els, texts) => {
      els.forEach((el, i) => {
        if (texts[i] !== undefined) el.textContent = texts[i];
      });
    }, said);
  }
  // The connect card's Claude Code tab has one line more than the board's: the key guide's discovery setting, which puts the
  // site's models in the client (packages/ui key-guide.ts). The card sits centred in its frame, so a line more moves it;
  // the line is hidden on the page for the comparison, as the board's omissions are on its side, and the lines after it
  // are numbered as the board's are.
  if (scene === "site connect")
    await page.locator(".hfm1 .ln", { hasText: "GATEWAY_MODEL_DISCOVERY" }).evaluate((line) => {
      line.style.setProperty("display", "none");
      for (let next = line.nextElementSibling; next; next = next.nextElementSibling) {
        const number = next.querySelector(".no");
        if (number?.textContent) number.textContent = String(Number(number.textContent) - 1);
      }
    });
  // the section's own top is where the two are lined up: what lies above it (the places' gaps) is not the screen's
  const top = (side: Page) => side.locator(`.hfm${sec} .sec`).evaluate((el) => el.getBoundingClientRect().top);
  const shift = (await top(page)) - (await top(board));
  await page.locator(".hs").evaluate((el, by) => el.scrollBy({ top: by }), shift);
};
type Face = { family: string; weight: string; axes?: { wght?: number | number[] } };
const { faces }: { faces: Face[] } = JSON.parse(
  readFileSync(new URL("../scripts/fonts/sources.json", import.meta.url), "utf8"),
);
// The faces an app ships for the board's families: the console and the website differ only in their cut of MiSans.
const productFaces = (cjk: string) =>
  Object.fromEntries(
    [
      ["Axiom Sans", "Axiom Sans"],
      ["Axiom Mono", "Axiom Mono"],
      ["Axiom CJK", cjk],
    ].map(([boardFamily, shippedFamily]) => [boardFamily, faces.find(({ family }) => family === shippedFamily)]),
  );
const consoleFonts = productFaces("Axiom CJK Console");

// The board draws in the app's faces at their pinned weight axes (useProductFontWeights): its source MiSans would draw
// the Regular at wght 400, where the shipped face is instanced at 330 (docs/frontend.md「字体」).
const inFaces = (fonts: ReturnType<typeof productFaces>) => async (board: Page) => {
  await board.evaluate(useProductFontWeights, fonts);
  await board.evaluate(() => document.fonts.ready.then(() => {}));
};
const consoleFaces = inFaces(consoleFonts);
const siteFaces = inFaces(productFaces("Axiom CJK"));

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
    await board.evaluate(useProductFontWeights, consoleFonts);
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

// Every board region's text in both languages, by the Chinese: the English reference of a page is its Chinese board with
// each text the regions carry replaced by its translation (packages/boards, generated with the regions).
async function regionTranslations(board: string) {
  const dir = new URL(`../packages/boards/src/${board}/regions/`, import.meta.url);
  const translations: Record<string, string> = {};
  for (const file of readdirSync(dir).filter((name) => name.endsWith(".zh.ts"))) {
    const zh = (await import(new URL(file, dir).href)).default as Record<string, string>;
    const en = (await import(new URL(file.replace(".zh.ts", ".en.ts"), dir).href)).default as Record<string, string>;
    for (const key of Object.keys(en)) if (zh[key] !== undefined) translations[zh[key]] = en[key] as string;
  }
  return translations;
}
const consoleTranslations = await regionTranslations("AxApp");
const loginTranslations = await regionTranslations("Login");

const translateBoard = (board: Page, strings: Record<string, string>) =>
  board.evaluate((strings) => {
    document.documentElement.lang = "en-US";
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      const translation = strings[node.textContent ?? ""];
      if (translation !== undefined) node.textContent = translation;
    }
  }, strings);

async function prepareConsoleEnglish(board: Page) {
  await translateBoard(board, consoleTranslations);
  await board.evaluate(useProductFontWeights, consoleFonts);
  await board.evaluate(() => document.fonts.ready.then(() => {}));
}

// The console boards' demo account is billed in yuan: the scene's account is put in yuan, its amounts unchanged, so the
// page formats its money, and picks its quick top-up amounts, as it does for such an account.
function inYuan(value: unknown) {
  if (Array.isArray(value)) {
    for (const item of value) inYuan(item);
  } else if (value && typeof value === "object") {
    const record = value as Record<string, unknown>;
    for (const [key, item] of Object.entries(record)) {
      if (key === "currency" && item === "USD") record[key] = "CNY";
      else inYuan(item);
    }
  }
}

// The overview board compares the month's spend with the same days of last month (「比 9 月同期 +23%」): the scene's
// account spent as much last month, less the board's 23%.
function lastMonthIn(state: unknown) {
  withLastMonth(state as MockState, 23);
  inYuan(state);
}

// The console's pages, each in both languages: [name, the e2e scene, the board's page]. The English scene is the
// Chinese board put into English (above) against the page in English, with the Chinese scene's rulings.
const chineseOnly = new Set(["overview", "usage", "bill", "notifications"]);
const consolePages: DesignScene[] = [
  ["overview", "overview", "ov"],
  ["usage", "usage", "usage"],
  ["route", "routing-presets", "route"],
  ["byok", "provider-keys", "byok"],
  ["keys", "keys", "keys"],
  ["bill", "balance", "bill"],
  ["calls", "calls", "logs"],
  ["settings", "settings", "profile"],
  ["security", "security", "security"],
  ["notifications", "notifications", "notify"],
].flatMap(([name, scene, page]) =>
  (["zh", "en"] as const)
    // Chinese only where the English page cannot match a translated board: the notification rows, the overview's and the
    // usage page's labels and comparisons are the page's own English copy, which wraps otherwise than the board's
    // translation; the bill's English writes yuan as CN¥ and its tabs push the description onto two lines. Their English
    // is gate.spec.ts's and a person's to check.
    .filter((language) => !(chineseOnly.has(name as string) && language === "en"))
    .map((language) => ({
      name: language === "en" ? `console ${name} en` : `console ${name}`,
      rulingsOf: `console ${name}`,
      suite: consoleSuite,
      scene: scene as string,
      fonts: "console",
      boards: consoleBoards(page as string),
      schemes: consoleSchemes,
      languages: [language],
      // both sides in grayscale, as the page's layers and the board's do not get the same subpixel text (the phone's
      // mark); Chinese also unhinted and in the console's faces, as the request presets' scene is
      textRasterization: language === "zh" ? ("unhinted" as const) : ("grayscale" as const),
      account: name === "overview" ? lastMonthIn : inYuan,
      prepareComparison: language === "en" ? prepareConsoleEnglish : consoleFaces,
    })),
);

export const designScenes: DesignScene[] = [
  // The shell on its own: the page inside it is ruled out here and compared in the page's scene. 820px is the tablet
  // board, the wide board's markup with its sidebar folded.
  {
    name: "console shell",
    suite: consoleSuite,
    scene: "overview",
    fonts: "console",
    boards: consoleBoards("ov"),
    schemes: consoleSchemes,
  },
  ...consolePages,
  ...presetScenes,
  {
    name: "console sign-in",
    suite: consoleSuite,
    scene: "sign-in",
    // the console's Latin faces, and the sign-in route's own: the Chinese and the serif the Login board names
    fonts: ["console", "console/auth/sign-in"],
    boards: loginBoards,
    schemes: { light: { mode: "浅色" }, dark: { mode: "深色" } },
    languages: ["zh", "en"],
    prepareComparison: (board, page, language) => prepareEmailStep(board, page, language, signInWords(language)),
  },
  {
    // the request for a reset link: the board draws no reset, and its email step is the same parts
    name: "console reset-password",
    suite: consoleSuite,
    scene: "reset-password",
    fonts: ["console", "console/auth/reset-password"],
    boards: loginBoards,
    schemes: { light: { mode: "浅色" }, dark: { mode: "深色" } },
    languages: ["zh", "en"],
    prepareComparison: (board, page, language) => prepareEmailStep(board, page, language, resetWords(language)),
  },
  {
    // open registration: the board draws no registration, and its email step is the same parts
    name: "console register",
    suite: consoleSuite,
    scene: "register-open",
    fonts: ["console", "console/auth/register"],
    boards: loginBoards,
    schemes: { light: { mode: "浅色" }, dark: { mode: "深色" } },
    languages: ["zh", "en"],
    prepareComparison: (board, page, language) => prepareEmailStep(board, page, language, registerWords(language)),
  },
  {
    name: "site home",
    suite: siteSuite,
    scene: "home",
    fonts: "site/home",
    boards: siteBoards,
    schemes: { light: { system: "浅色" }, dark: { system: "深色" } },
    // as the console's Chinese pages: grayscale on both sides, as the page's layers and the board's get different
    // subpixel text, and unhinted, so the cut MiSans and the board's source face draw the same strokes
    textRasterization: "unhinted",
    prepareComparison: siteFaces,
  },
  ...[
    { name: "site connect", sec: 1 },
    { name: "site faq", sec: 5 },
    { name: "site bed", sec: 6 },
  ].map(({ name, sec }) => ({
    name,
    suite: siteSuite,
    scene: "home",
    fonts: "site/home",
    boards: siteBoards,
    schemes: { light: { system: "浅色" }, dark: { system: "深色" } },
    textRasterization: "unhinted" as const,
    prepareComparison: async (board: Page, page: Page) => {
      await siteFaces(board);
      await screenAt(sec, name)(board, page);
    },
  })),
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
