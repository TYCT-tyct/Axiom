import { describe, expect, it } from "vitest";

// faceTexts traces a whole app's imports and messages: seconds of work that grow with the code, not a wait on anything.
// A shared CI machine under load took longer than vitest's 5 s default for each of these (train 44817932330-1).
const tracing = { timeout: 60_000 };
import { exported, names, routeLiterals } from "./routes.ts";
import { faceTexts, readManifest } from "./text.ts";

describe("names", () => {
  it.each([
    ["the key itself", ["redeem.title"], "redeem.title", true],
    ["a plural form of the key", ["routingPresets.keys"], "routingPresets.keys_one", true],
    ["a key under a dotted literal", ["settings.mail"], "settings.mail.save", true],
    ["a key under a template literal's start", ["codes."], "codes.not_found", true],
    ["a key under a bare word, which only names itself", ["balance"], "balance.title", false],
    ["a sibling key", ["redeem.title"], "redeem.intro", false],
  ])("%s", (_, literals, key, expected) => {
    expect(names(new Set(literals), key)).toBe(expected);
  });
});

describe("exported", () => {
  const exports = {
    "./Login/regions/*": "./src/Login/regions/*.tsx",
    "./Login/regions/*.zh": "./src/Login/regions/*.zh.ts",
    "./Login/regions/*.en": "./src/Login/regions/*.en.ts",
  };
  it.each([
    ["a region", "./Login/regions/LoginShell", "./src/Login/regions/LoginShell.tsx"],
    ["a region's Chinese text", "./Login/regions/LoginShell.zh", "./src/Login/regions/LoginShell.zh.ts"],
    ["a region's English text", "./Login/regions/LoginShell.en", "./src/Login/regions/LoginShell.en.ts"],
  ])("resolves %s through the longest pattern that matches", (_, key, target) => {
    expect(exported(exports, key)).toBe(target);
  });
});

describe("routeLiterals", () => {
  const pages = routeLiterals("console", "console/src/main.tsx");

  it("gives each lazily loaded page the keys its own code names, and not another page's", () => {
    expect(pages.get("console/pages/redeem")?.literals.has("redeem.title")).toBe(true);
    expect(pages.get("console/auth/sign-in")?.literals.has("redeem.title")).toBe(false);
  });

  it("follows a package's index only to the exports a page imports", () => {
    // The rules editor is exported from @axiom/app, which the shell imports for other names.
    expect(pages.get("console/pages/routing-presets")?.literals.has("presetRules.sort")).toBe(true);
    expect(pages.get("console/auth/sign-in")?.literals.has("presetRules.sort")).toBe(false);
  });

  it("gives every page the shell's keys", () => {
    for (const { literals } of pages.values()) {
      expect(literals.has("shell.path")).toBe(true);
    }
  });
});

describe("faceTexts", () => {
  it("cuts a page's plural messages into its characters", tracing, async () => {
    // admin/pages/redeem-codes shows redeem.issuedIntro, which has only plural forms: 「这是唯一一次显示」.
    // Only the face under test, so the run reads the admin's messages and not every board's region words.
    const manifest = readManifest();
    const face = manifest.faces.find((one) => one.family === "Axiom CJK Admin");
    const [texts] = await faceTexts({ ...manifest, faces: face ? [face] : [] });
    expect(texts?.["admin/pages/redeem-codes"]).toContain("唯");
  });
});

describe("region words", () => {
  // The console's pages are cut from board regions, whose words are not in the app's messages: a page gets the words of
  // the regions it runs (its own and the shell's), and not every region's.
  it("go to the pages that run their region, and not to others", tracing, async () => {
    const manifest = readManifest();
    const face = manifest.faces.find((one) => one.family === "Axiom CJK Console");
    const [texts] = await faceTexts({ ...manifest, faces: face ? [face] : [] });
    const { default: keys } = await import("../../packages/boards/src/AxApp/regions/Keys.zh.ts");
    const words = [...new Set(Object.values(keys).join(""))].filter((c) => /\p{Script=Han}/u.test(c));
    expect(words.filter((c) => !texts?.["console/pages/keys"]?.includes(c))).toEqual([]);
    expect(words.some((c) => !texts?.["console/auth/sign-in"]?.includes(c))).toBe(true);
  });
});

describe("the website's pages", () => {
  // The models page is its own document: its words are the ones its code runs, not the home page's questions; the
  // first screen's words are a page of their own, the slice the shared entry page preloads.
  it("give the models page its own words and the first screen only its own", tracing, async () => {
    const manifest = readManifest();
    const face = manifest.faces.find((one) => one.family === "Axiom CJK");
    const [texts] = await faceTexts({ ...manifest, faces: face ? [face] : [] });
    const { default: zh } = await import("../../site/src/locales/zh.ts");
    const faq = [
      ...new Set(
        Object.values(zh.script.faq)
          .flatMap((v) => (typeof v === "string" ? [v] : Object.values(v)))
          .join(""),
      ),
    ];
    const han = (c: string) => /\p{Script=Han}/u.test(c);
    expect(faq.filter(han).some((c) => !texts?.["site/models"]?.includes(c))).toBe(true);
    expect(faq.filter(han).some((c) => !texts?.["site/first"]?.includes(c))).toBe(true);
    expect([...zh.signIn].every((c) => texts?.["site/first"]?.includes(c))).toBe(true);
  });
});
