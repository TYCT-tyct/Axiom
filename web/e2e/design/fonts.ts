import { readFileSync } from "node:fs";
import { join } from "node:path";
import type { Page, Route } from "@playwright/test";
import { sourceFont } from "../../packages/boards/fonts.ts";

// The fonts of both halves of a comparison. The board is drawn with the unsubset source fonts the pipeline cuts from
// (scripts/fonts/sources.json, checked by SHA-256), not with the build's subsets: a character a subset lacks then shows
// as a difference instead of both sides falling back to the same system font (docs/frontend.md「决策与理由」).

// A face a side must have loaded, and characters to ask for: a family whose face fails to load, or that has no face
// covering them, would be drawn in a system font.
export type FontFace = { family: string; style: string; weight: string; sample: string };

const web = join(import.meta.dirname, "..", "..");
const sources: Record<string, { sha256: string }> = JSON.parse(
  readFileSync(join(web, "scripts/fonts/sources.json"), "utf8"),
).sources;
type Built = {
  family: string;
  style: string;
  weight: string;
  pages: string[];
  files: { pages: string[]; chars: string }[];
};
const built: { faces: Built[] } = JSON.parse(readFileSync(join(web, "packages/ui/src/css/fonts/fonts.json"), "utf8"));

// The board's families and the source each stands for. The board names its own families; the build names them in
// fonts.css. A family the board draws text in that is missing here makes usedFaces fail rather than fall back.
const boardSources: Record<string, { source: string; sample: string }> = {
  "Axiom Sans": { source: "instrument-sans", sample: "Axiom 0123" },
  "Axiom Mono": { source: "jetbrains-mono", sample: "sk-0123" },
  "Axiom CJK": { source: "cjk-body", sample: "控制台" },
  "AX Serif": { source: "cjk-serif", sample: "控制台" },
  "AX Serif Latin": { source: "instrument-serif-italic", sample: "key" },
};

// The pinned MiSans source is an archive; extract its declared member before serving it as a font.
export async function fontRoute(route: Route, name: string) {
  if (!sources[name]) return route.abort("blockedbyclient");
  return route.fulfill({ contentType: "font/ttf", body: await sourceFont(name) });
}

const descriptor = (rule: string, name: string) =>
  rule
    .match(new RegExp(`${name}\\s*:\\s*([^;}]+)`))?.[1]
    ?.trim()
    .replace(/^(['"])(.*)\1$/, "$2");

// System fonts a board's stacks name after one of its own, for characters its own lack. Only these may stay without a
// source font, and only behind another family.
const systemFallbacks = ["PingFang SC"];

const fontFace = /@font-face\s*\{[^}]*\}/g;

// boardFonts replaces the board's @font-face rules (they point at the design tool's blob store, unreachable here) with
// one rule for each family that has a source font, keeping the board's weight, style and size-adjust where it declares
// the family, on the source font served from base. A browser fetches a face only when text uses it; which families the
// board uses is read from the rendered page (usedFaces), not from its CSS, which can name them through custom
// properties, without quotes or from script.
export function boardFonts(helmet: string, base: string) {
  const declared = new Map<string, string>();
  for (const rule of helmet.match(fontFace) ?? []) {
    const family = descriptor(rule, "font-family");
    if (family && !declared.has(family)) declared.set(family, rule);
  }
  const faces: FontFace[] = [];
  const css: string[] = [];
  for (const [family, { source, sample }] of Object.entries(boardSources)) {
    const rule = declared.get(family) ?? "";
    const face = {
      family,
      style: descriptor(rule, "font-style") ?? "normal",
      weight: descriptor(rule, "font-weight") ?? "400",
      sample,
    };
    const sizeAdjust = descriptor(rule, "size-adjust");
    css.push(
      `@font-face{font-family:'${family}';font-style:${face.style};font-weight:${face.weight};font-display:block;` +
        `${sizeAdjust ? `size-adjust:${sizeAdjust};` : ""}src:url(${base}${source})}`,
    );
    faces.push(face);
  }
  const rest = helmet.replace(fontFace, "").replace(/<link[^>]*>/g, "");
  return { helmet: `<style>${css.join("\n")}</style>${rest}`, faces };
}

// usedFaces reads the computed font-family of everything that draws text: elements with text of their own, form
// controls, ::before and ::after whose content is text, and list markers. var(), unquoted names and styles set by script
// are all resolved by the browser; text with no font of its own reports the browser's default, which fails. It returns
// the faces of the used families with a source font, and fails on a family with no source font unless it is a generic
// family or a system fallback behind another: that text would be drawn in a font the reference does not have.
export async function usedFaces(page: Page, faces: FontFace[]) {
  const stacks = await page.evaluate(() => {
    const ownText = (el: Element) =>
      el.matches(
        'input:not([type="checkbox"], [type="radio"], [type="range"], [type="color"], [type="hidden"]), textarea, select',
      ) || [...el.childNodes].some((node) => node.nodeType === Node.TEXT_NODE && node.textContent?.trim());
    // Generated text is no text node: content with a non-empty string, counter() or attr() draws characters (computed
    // style serializes strings in double quotes); an empty string only makes a decorative box.
    const generated = (el: Element) =>
      ["::before", "::after"]
        .map((pseudo) => getComputedStyle(el, pseudo))
        .filter((style) => /"(?:[^"\\]|\\.)+"|counters?\(|attr\(/.test(style.content));
    const marker = (el: Element) => {
      const style = getComputedStyle(el);
      return style.display === "list-item" && style.listStyleType !== "none" ? [getComputedStyle(el, "::marker")] : [];
    };
    const drawn = (el: Element) => [...(ownText(el) ? [getComputedStyle(el)] : []), ...generated(el), ...marker(el)];
    const elements = [...document.body.querySelectorAll("*")].filter(
      (el) => !el.matches("script, style, template, noscript"),
    );
    return [...new Set(elements.flatMap(drawn).map((style) => style.fontFamily))];
  });
  const generic =
    /^(serif|sans-serif|monospace|cursive|fantasy|system-ui|emoji|math|fangsong|ui-(serif|sans-serif|monospace|rounded))$/;
  const used = new Set<string>();
  for (const stack of stacks) {
    for (const [index, family] of stack
      .split(",")
      .map((name) => name.trim().replace(/^(['"])(.*)\1$/, "$2"))
      .entries()) {
      if (boardSources[family]) used.add(family);
      else if (!generic.test(family) && (index === 0 || !systemFallbacks.includes(family))) {
        throw new Error(`the board uses the font ${family}, which has no source font`);
      }
    }
  }
  return faces.filter((face) => used.has(face.family));
}

// The faces fonts.json gives a page of the build ("console", "site/home"...), each asked for with characters it holds.
export function pageFonts(name: string): FontFace[] {
  return built.faces
    .filter((face) => face.pages.includes(name))
    .map((face) => ({
      family: face.family,
      style: face.style,
      weight: face.weight,
      sample:
        face.files
          .find((file) => file.pages.includes(name))
          ?.chars.replace(/\s/g, "")
          .slice(0, 8) ?? "",
    }));
}

// fontsLoaded fails unless every face loaded and covers its sample. document.fonts.ready and document.fonts.check() are
// both satisfied by a family that has no face at all (it is drawn in a fallback), so each face is loaded explicitly and
// the faces the browser answers with are counted.
export async function fontsLoaded(page: Page, faces: FontFace[]) {
  const failures = await page.evaluate(async (wanted) => {
    const failed: string[] = [];
    for (const face of wanted) {
      const font = `${face.style} ${face.weight.split(" ")[0]} 16px "${face.family}"`;
      try {
        if ((await document.fonts.load(font, face.sample)).length === 0) failed.push(`${font}: no face`);
      } catch (error) {
        failed.push(`${font}: ${String(error)}`);
      }
    }
    return failed;
  }, faces);
  if (failures.length) {
    throw new Error(`fonts did not load, the shot would show a system font: ${failures.join("; ")}`);
  }
}
