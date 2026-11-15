import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import type { Page, Route } from "@playwright/test";

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
// fonts.css. A family the board's CSS uses that is missing here makes boardFonts fail rather than fall back.
const boardSources: Record<string, { source: string; sample: string }> = {
  "Axiom Sans": { source: "instrument-sans", sample: "Axiom 0123" },
  "Axiom Mono": { source: "jetbrains-mono", sample: "sk-0123" },
  "Axiom CJK": { source: "cjk-body", sample: "控制台" },
  "AX Serif": { source: "cjk-serif", sample: "控制台" },
  "AX Serif Latin": { source: "instrument-serif-italic", sample: "key" },
};

const cache = join(process.env.XDG_CACHE_HOME || join(homedir(), ".cache"), "axiom-fonts");

// fontRoute answers a board's request for a source font from the pipeline's download cache, after checking its hash.
export function fontRoute(route: Route, name: string) {
  const source = sources[name];
  if (!source) {
    return route.abort("blockedbyclient");
  }
  const file = join(cache, `${source.sha256}-${name}`);
  let body: Buffer;
  try {
    body = readFileSync(file);
  } catch {
    throw new Error(`${file} is missing: run pnpm --dir web fonts once to download the source fonts`);
  }
  if (createHash("sha256").update(body).digest("hex") !== source.sha256) {
    throw new Error(`${file} does not match its SHA-256 in scripts/fonts/sources.json`);
  }
  return route.fulfill({ contentType: "font/ttf", body });
}

const descriptor = (rule: string, name: string) =>
  rule
    .match(new RegExp(`${name}\\s*:\\s*([^;}]+)`))?.[1]
    ?.trim()
    .replace(/^(['"])(.*)\1$/, "$2");

// System fonts a board's stacks name after one of its own, for characters its own lack. Only these may stay without a
// source font, and only behind another family.
const systemFallbacks = new Set(["PingFang SC"]);

const fontFace = /@font-face\s*\{[^}]*\}/g;

// used lists the quoted families in the font and font-family declarations of the board's CSS, without @font-face rules,
// failing on one with no source font: the board would be drawn in a fallback. Stacks of custom properties (var(--mono))
// name no family here.
function used(css: string) {
  const families = new Set<string>();
  for (const [, value] of css.matchAll(/font(?:-family)?\s*:\s*([^;}]+)/g)) {
    for (const [index, [, , family]] of [...(value as string).matchAll(/(['"])([^'"]+)\1/g)].entries()) {
      if (boardSources[family as string]) families.add(family as string);
      else if (index === 0 || !systemFallbacks.has(family as string)) {
        throw new Error(`the board uses the font ${family}, which has no source font`);
      }
    }
  }
  return families;
}

// boardFonts replaces the board's @font-face rules (they point at the design tool's blob store, unreachable here) with
// one rule per family it uses or declares that has a source font, keeping the board's weight, style and size-adjust, on
// the source font served from base. Every such family is a face fontsLoaded must load.
export function boardFonts(helmet: string, base: string) {
  const declared = new Map<string, string>();
  for (const rule of helmet.match(fontFace) ?? []) {
    const family = descriptor(rule, "font-family");
    if (family && !declared.has(family)) declared.set(family, rule);
  }
  const rest = helmet.replace(fontFace, "").replace(/<link[^>]*>/g, "");
  // Declared but unused families without a source are dropped: some boards carry the design tool's whole font list.
  const families = new Set([...used(rest), ...[...declared.keys()].filter((family) => boardSources[family])]);
  const faces: FontFace[] = [];
  const css: string[] = [];
  for (const family of families) {
    const rule = declared.get(family) ?? "";
    const face = {
      family,
      style: descriptor(rule, "font-style") ?? "normal",
      weight: descriptor(rule, "font-weight") ?? "400",
      sample: (boardSources[family] as { sample: string }).sample,
    };
    const sizeAdjust = descriptor(rule, "size-adjust");
    css.push(
      `@font-face{font-family:'${family}';font-style:${face.style};font-weight:${face.weight};font-display:block;` +
        `${sizeAdjust ? `size-adjust:${sizeAdjust};` : ""}src:url(${base}${boardSources[family]?.source})}`,
    );
    faces.push(face);
  }
  return { helmet: `<style>${css.join("\n")}</style>${rest}`, faces };
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
