import { describe, expect, it } from "vitest";
import { names, routeLiterals } from "./routes.ts";
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

describe("routeLiterals", () => {
  const pages = routeLiterals("console", "console/src/main.tsx");

  it("gives each lazily loaded page the keys its own code names, and not another page's", () => {
    expect(pages.get("console/pages/redeem")?.has("redeem.title")).toBe(true);
    expect(pages.get("console/auth/sign-in")?.has("redeem.title")).toBe(false);
  });

  it("follows a package's index only to the exports a page imports", () => {
    // The rules editor is exported from @axiom/app, which the shell imports for other names.
    expect(pages.get("console/pages/routing-presets")?.has("presetRules.sort")).toBe(true);
    expect(pages.get("console/auth/sign-in")?.has("presetRules.sort")).toBe(false);
  });

  it("gives every page the shell's keys", () => {
    for (const literals of pages.values()) {
      expect(literals.has("shell.path")).toBe(true);
    }
  });
});

describe("faceTexts", () => {
  it("cuts a page's plural messages into its characters", async () => {
    // admin/pages/redeem-codes shows redeem.issuedIntro, which has only plural forms: 「这是唯一一次显示」.
    // Only the face under test, so the run reads the admin's messages and not every board's region words.
    const manifest = readManifest();
    const face = manifest.faces.find((one) => one.family === "Axiom CJK Admin");
    const [texts] = await faceTexts({ ...manifest, faces: face ? [face] : [] });
    expect(texts?.["admin/pages/redeem-codes"]).toContain("唯");
  });
});
