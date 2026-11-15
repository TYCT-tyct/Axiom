// The characters each font face must cover on each page, computed from the translation files: the pipeline subsets the
// fonts to them (build.py runs this file and reads its output), and coverage.test.ts checks the committed fonts still
// cover them. One definition, so the two cannot disagree on what a page shows.
import { readFileSync } from "node:fs";
import { registerHooks } from "node:module";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const web = join(import.meta.dirname, "..", "..");

// Each namespace's message files, Chinese then English, as the apps load them (i18n.ts startI18n).
const namespaces: Record<string, string[]> = {
  common: ["packages/ui/src/locales/zh.ts", "packages/ui/src/locales/en.ts"],
  site: ["site/src/locales/zh.ts", "site/src/locales/en.ts"],
  console: ["console/src/locales/zh.ts", "console/src/locales/en.ts"],
  admin: ["admin/src/locales/zh/index.ts", "admin/src/locales/en/index.ts"],
};

// A page names, per namespace, the message keys it can show: "" is every key, "hero" is hero and everything under it.
type Selection = Record<string, string[]>;
// A face covers either the literal characters it lists or one script of its pages' messages, optionally only of the
// keys it selects (a heading face covers the headings only).
type FaceText = { chars: string } | { script: Script; keys?: Selection };
type Script = "latin" | "han";
type Face = { family: string; weight: string; style?: string; pages: string[]; text: FaceText };
export type Manifest = { pages: Record<string, Selection>; faces: Face[] };

const manifestPath = join(import.meta.dirname, "sources.json");

// Han ideographs with the CJK punctuation and full-width forms set beside them; everything else is set in the Latin
// faces, which fall back to the system's font for what they lack.
const han = /[\p{Script=Han}　-〿＀-￯]/u;
const ascii = Array.from({ length: 0x7f - 0x20 }, (_, i) => String.fromCodePoint(0x20 + i)).join("");

function flatten(value: unknown, prefix: string, out: Map<string, string>) {
  if (typeof value === "string") {
    out.set(prefix, value);
  } else if (value && typeof value === "object") {
    for (const [key, child] of Object.entries(value)) {
      flatten(child, prefix ? `${prefix}.${key}` : key, out);
    }
  }
  return out;
}

async function messages(namespace: string) {
  const files = namespaces[namespace];
  if (!files) {
    throw new Error(`${manifestPath}: no namespace ${namespace}; known: ${Object.keys(namespaces).join(", ")}`);
  }
  const out: [string, string][] = [];
  for (const file of files) {
    const { default: tree } = await import(pathToFileURL(join(web, file)).href);
    out.push(...flatten(tree, "", new Map()));
  }
  return out;
}

const selected = (key: string, prefixes: string[]) =>
  prefixes.some((p) => p === "" || key === p || key.startsWith(`${p}.`));

async function selectedText(selection: Selection) {
  let text = "";
  for (const [namespace, prefixes] of Object.entries(selection)) {
    for (const [key, value] of await messages(namespace)) {
      if (selected(key, prefixes)) {
        text += value;
      }
    }
  }
  return text;
}

const uniqueSorted = (text: string) =>
  [...new Set(text)].sort((a, b) => (a.codePointAt(0) ?? 0) - (b.codePointAt(0) ?? 0)).join("");

// faceTexts returns, for each face in order, the sorted characters it must cover on each of its pages.
export async function faceTexts(manifest: Manifest): Promise<Record<string, string>[]> {
  return Promise.all(
    manifest.faces.map(async (face) => {
      const perPage: Record<string, string> = {};
      for (const page of face.pages) {
        const selection = manifest.pages[page];
        if (!selection) {
          throw new Error(`${manifestPath}: ${face.family} ${face.weight} names the unknown page ${page}`);
        }
        const { text } = face;
        if ("chars" in text) {
          perPage[page] = uniqueSorted(text.chars);
          continue;
        }
        const chars = [...(await selectedText(text.keys ?? selection))].filter(
          (c) => han.test(c) === (text.script === "han"),
        );
        perPage[page] = uniqueSorted((text.script === "latin" ? ascii : "") + chars.join(""));
      }
      return perPage;
    }),
  );
}

export const readManifest = (): Manifest => JSON.parse(readFileSync(manifestPath, "utf8"));

// As a command it prints the faces' characters as JSON for build.py. The apps import their messages without file
// extensions, which Vite resolves; plain Node needs to be told to try .ts.
if (import.meta.main) {
  registerHooks({
    resolve(specifier, context, next) {
      try {
        return next(specifier, context);
      } catch (error) {
        if (specifier.startsWith(".")) {
          return next(`${specifier}.ts`, context);
        }
        throw error;
      }
    },
  });
  process.stdout.write(JSON.stringify(await faceTexts(readManifest())));
}
