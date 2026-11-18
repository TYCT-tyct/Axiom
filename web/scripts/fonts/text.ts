// The characters each font face must cover on each page, computed from the translation files: the pipeline subsets the
// fonts to them (build.py runs this file and reads its output), and coverage.test.ts checks the committed fonts still
// cover them. One definition, so the two cannot disagree on what a page shows.
import { globSync, readFileSync } from "node:fs";
import { registerHooks } from "node:module";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { names, routeLiterals } from "./routes.ts";

const web = join(import.meta.dirname, "..", "..");

// Each namespace's message files, Chinese then English, as the apps load them (i18n.ts startI18n).
// The screens' words are not in the apps' message files: they come with the generated regions of the boards the pages
// are cut from (packages/boards/src/<Board>/regions/*.zh.ts and .en.ts, by hash keys), which a page shows whole.
const regions = (...boards: string[]) =>
  boards.flatMap((board) => ["zh", "en"].map((language) => `packages/boards/src/${board}/regions/*.${language}.ts`));
const namespaces: Record<string, string[]> = {
  "site-regions": regions("Site", "SitePhone", "SiteTablet", "SiteTabletWide"),
  "console-regions": regions("AxApp", "AxAppPhone", "AxAppDark", "AxAppPhoneDark", "AxAppTablet", "AxDS"),
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
// A face's pages name pages, or "<app>/*" for each page of an app the manifest's routes name (routePages).
type Face = { family: string; weight: string; style?: string; pages: string[]; text: FaceText };
export type Manifest = { pages: Record<string, Selection>; routes?: Record<string, string>; faces: Face[] };

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

const loaded = new Map<string, Promise<[string, string][]>>();
const messages = (namespace: string) => {
  const cached = loaded.get(namespace) ?? load(namespace);
  loaded.set(namespace, cached);
  return cached;
};

async function load(namespace: string) {
  const files = namespaces[namespace];
  if (!files) {
    throw new Error(`${manifestPath}: no namespace ${namespace}; known: ${Object.keys(namespaces).join(", ")}`);
  }
  const out: [string, string][] = [];
  for (const file of files.flatMap((pattern) =>
    pattern.includes("*") ? globSync(pattern, { cwd: web }).sort() : [pattern],
  )) {
    const { default: tree } = await import(pathToFileURL(join(web, file)).href);
    out.push(...flatten(tree, "", new Map()));
  }
  return out;
}

// selected tells whether a key is one of the prefixes or under one; a plural form (keys_one) counts as its base key,
// which is what code names when it passes a count, and as itself, which is how a route's selection lists it.
function selected(key: string, prefixes: Set<string>) {
  if (prefixes.has("") || prefixes.has(key)) {
    return true;
  }
  for (let k = key.replace(/_(zero|one|two|few|many|other)$/, ""); ; k = k.slice(0, k.lastIndexOf("."))) {
    if (prefixes.has(k)) {
      return true;
    }
    if (!k.includes(".")) {
      return false;
    }
  }
}

async function selectedText(selection: Selection) {
  let text = "";
  for (const [namespace, prefixes] of Object.entries(selection)) {
    const set = new Set(prefixes);
    for (const [key, value] of await messages(namespace)) {
      if (selected(key, set)) {
        text += value;
      }
    }
  }
  return text;
}

// routePages returns an app's pages ("console/pages/keys") and the keys each can show, in each of its namespaces.
async function routePages(app: string, entry: string, namespaces: string[]) {
  const pages = new Map<string, Selection>();
  for (const [page, literals] of routeLiterals(app, entry)) {
    const selection: Selection = {};
    for (const namespace of namespaces) {
      // a page shows its board's region words whole ("" is every key); the code names no key of them
      selection[namespace] = namespace.endsWith("-regions")
        ? [""]
        : (await messages(namespace)).map(([key]) => key).filter((key) => names(literals, key));
    }
    pages.set(page, selection);
  }
  return pages;
}

// pageSelections returns every page the manifest names with its selection, an app's routed pages included.
async function pageSelections(manifest: Manifest) {
  const pages = new Map(Object.entries(manifest.pages));
  for (const [app, entry] of Object.entries(manifest.routes ?? {})) {
    const selection = manifest.pages[app];
    if (!selection) {
      throw new Error(`${manifestPath}: routes names ${app}, which pages does not define`);
    }
    for (const [page, routed] of await routePages(app, entry, Object.keys(selection))) {
      pages.set(page, routed);
    }
  }
  return pages;
}

const uniqueSorted = (text: string) =>
  [...new Set(text)].sort((a, b) => (a.codePointAt(0) ?? 0) - (b.codePointAt(0) ?? 0)).join("");

// faceTexts returns, for each face in order, the sorted characters it must cover on each of its pages.
export async function faceTexts(manifest: Manifest): Promise<Record<string, string>[]> {
  const selections = await pageSelections(manifest);
  const expand = (page: string) => {
    const pages = page.endsWith("/*") ? [...selections.keys()].filter((p) => p.startsWith(page.slice(0, -1))) : [page];
    if (pages.length === 0) {
      throw new Error(`${manifestPath}: ${page} names no page; routes names the apps whose pages it finds`);
    }
    return pages;
  };
  return Promise.all(
    manifest.faces.map(async (face) => {
      const perPage: Record<string, string> = {};
      for (const page of face.pages.flatMap(expand)) {
        const selection = selections.get(page);
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
