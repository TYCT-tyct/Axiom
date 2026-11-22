// Fails when a built app loads more than its budget (docs/frontend.md「前端性能指标」): the script the first screen
// loads, the script each further page or lazily loaded part adds, everything the website loads after its first screen
// together (its deferred animations and their assets), and the fonts each page can load (font-budget.ts). All of it
// follows the build's real module graph: the manifest's imports and dynamic imports. Scripts count at their
// brotli size, the transfer size (scripts/compress.ts, as the program sends them); fonts are already compressed. The
// fonts the first screen preloads are reported beside them, and must be the ones fonts.json marks. Run after
// `pnpm build`.
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { brotli } from "./compress.ts";
import { type FontFile, type FontManifest, fontBudget } from "./font-budget.ts";

const kB = 1000;
// firstPage names the lazily loaded page the entry address opens (its manifest key): the first screen loads it too.
// deferred caps what the first screen loads later, all together (port design §8.3). The site's is sized for every
// effect the final home boards carry (sea, entrance, whale, Clawd, the sea's moments and terminal), which together
// measured about 156 kB; the rest is headroom for the boards' remaining effects, not for unrelated code.
// apart is a part loaded later that has a budget of its own, counted outside deferred: what its entries reach. The
// website holds the console's sign-in pages (site/src/home/sign-in.tsx): the host with the console's session and
// requests, the tiles transition, the solid on the sea and every sign-in step, all loaded once the first screen is
// drawn so a press moves at once. They measured about 95 kB; the cap is the first screen's own, so the sign-in pages,
// fetched while the visitor reads, never cost more than the screen they read.
type Apart = { what: string; entries: string[]; budget: number };
const budgets: Record<
  string,
  { firstScreen: number; perPage?: number; firstPage?: string; deferred?: number; apart?: Apart }
> = {
  site: {
    firstScreen: 150 * kB,
    perPage: 50 * kB,
    deferred: 170 * kB,
    apart: {
      what: "the sign-in pages",
      entries: ["src/home/sign-in-host.tsx", "../packages/fx/src/tiles.ts", "../packages/fx/src/login.ts"],
      budget: 150 * kB,
    },
  },
  console: { firstScreen: 200 * kB, perPage: 50 * kB, firstPage: "src/pages/overview.tsx" },
  admin: { firstScreen: 200 * kB, perPage: 50 * kB, firstPage: "src/pages/home.tsx" },
};
const fonts: FontManifest = JSON.parse(
  readFileSync(join(import.meta.dirname, "..", "packages/ui/src/css/fonts/fonts.json"), "utf8"),
);

export type Chunk = {
  file: string;
  isEntry?: boolean;
  isDynamicEntry?: boolean;
  imports?: string[];
  dynamicImports?: string[];
  assets?: string[];
};

// The size the program sends: the same compression embed.ts ships.
function brotliSize(path: string) {
  return brotli(readFileSync(path)).length;
}

// closure returns the chunk keys a chunk loads before it runs: itself and its static imports, recursively.
export function closure(manifest: Record<string, Chunk>, key: string, seen = new Set<string>()) {
  if (!seen.has(key)) {
    seen.add(key);
    for (const imported of manifest[key]?.imports ?? []) {
      closure(manifest, imported, seen);
    }
  }
  return seen;
}

// deferred returns what an app can load after its first screen: every chunk a dynamic import reaches from it, with
// what those chunks import, statically or dynamically, less what the first screen has; and the files those chunks
// emit (an image, a drawing), less fonts, which have a budget of their own.
export function deferred(manifest: Record<string, Chunk>, firstScreen: Set<string>) {
  const chunks = new Set<string>();
  const queue = [...firstScreen].flatMap((key) => manifest[key]?.dynamicImports ?? []);
  for (let key = queue.pop(); key !== undefined; key = queue.pop()) {
    for (const reached of closure(manifest, key)) {
      if (!firstScreen.has(reached) && !chunks.has(reached)) {
        chunks.add(reached);
        queue.push(...(manifest[reached]?.dynamicImports ?? []));
      }
    }
  }
  const assets = new Set(
    [...chunks].flatMap((key) => manifest[key]?.assets ?? []).filter((file) => !file.endsWith(".woff2")),
  );
  return { chunks, assets };
}

const failures: string[] = [];
function measure(what: string, size: number, budget: number) {
  const line = `${what.padEnd(36)} ${(size / kB).toFixed(1).padStart(6)} kB of ${budget / kB} kB`;
  console.log(line);
  if (size > budget) {
    failures.push(line);
  }
}

// scriptFiles returns the files, relative to dist, of the scripts the built entry page names: the module entry and the
// classic ones the manifest does not list, such as theme.js (vite.shared.ts).
function scriptFiles(dist: string, base: string) {
  const html = readFileSync(join(dist, "index.html"), "utf8");
  return [...html.matchAll(/<script\b[^>]*\bsrc="([^"]+)"/g)].map(([, src = ""]) => {
    if (!src.startsWith(base)) {
      throw new Error(`${dist}: the entry page loads ${src}, which is not under ${base}`);
    }
    return src.slice(base.length);
  });
}

if (import.meta.main) {
  for (const [app, budget] of Object.entries(budgets)) {
    const dist = join(import.meta.dirname, "..", app, "dist");
    const manifest: Record<string, Chunk> = JSON.parse(readFileSync(join(dist, ".vite", "manifest.json"), "utf8"));
    const { base = "/" } = (await import(pathToFileURL(join(dist, "..", "vite.config.ts")).href)).default;
    const files = (keys: Iterable<string>) => [...keys].map((key) => (manifest[key] as Chunk).file);
    const size = (paths: Iterable<string>) =>
      [...paths].reduce((total, file) => total + brotliSize(join(dist, file)), 0);

    const firstScreen = new Set<string>();
    for (const [key, chunk] of Object.entries(manifest)) {
      if (chunk.isEntry || key === budget.firstPage) {
        closure(manifest, key, firstScreen);
      }
    }
    if (budget.firstPage && !manifest[budget.firstPage]) {
      failures.push(`${app}: the first page ${budget.firstPage} is not in the build; update its name`);
    }
    // A file both the manifest and the entry page name counts once.
    measure(
      `${app}: first screen script`,
      size(new Set([...files(firstScreen), ...scriptFiles(dist, base)])),
      budget.firstScreen,
    );

    let loaded = firstScreen;
    if (budget.apart) {
      const { what, entries } = budget.apart;
      const missing = entries.filter((key) => !manifest[key]);
      if (missing.length) failures.push(`${app}: ${missing.join(", ")} not in the build; update ${what}' entries`);
      // the entries and what they load themselves (the host's sign-in steps), not what the code they share with the
      // homepage loads (@axiom/fx's effects, which deferred counts)
      const own = new Set<string>();
      for (const key of entries.flatMap((entry) => [entry, ...(manifest[entry]?.dynamicImports ?? [])])) {
        closure(manifest, key, own);
      }
      const chunks = [...own].filter((key) => !firstScreen.has(key));
      measure(`${app}: ${what}`, size(files(chunks)), budget.apart.budget);
      loaded = new Set([...firstScreen, ...own]);
    }
    if (budget.deferred) {
      const later = deferred(manifest, loaded);
      measure(`${app}: after the first screen`, size([...files(later.chunks), ...later.assets]), budget.deferred);
    }

    if (budget.perPage) {
      for (const [key, chunk] of Object.entries(manifest)) {
        if (chunk.isDynamicEntry && key !== budget.firstPage) {
          const added = [...closure(manifest, key)].filter((page) => !firstScreen.has(page));
          measure(`${app}: page ${chunk.file.replace(/^assets\//, "")}`, size(files(added)), budget.perPage);
        }
      }
    }

    // Vite names an emitted file after its source, then a hash: a page's font missing from the build means fonts.css is
    // not part of it, and the budget below would count fonts the app never ships.
    const assets = readdirSync(join(dist, "assets"));
    const ofApp = (file: FontFile) => file.pages.some((page) => page.split("/")[0] === app);
    const stem = (file: FontFile) => `${file.file.replace(/\.woff2$/, "")}-`;
    for (const file of fonts.faces.flatMap((face) => face.files).filter(ofApp)) {
      if (!assets.some((asset) => asset.startsWith(stem(file)))) {
        failures.push(`${app}: the build has no ${file.file}; is css/fonts.css imported?`);
      }
    }
    // The entry page preloads exactly the fonts fonts.json marks for the app's first screen (vite.shared.ts): the
    // website's first-screen Chinese slice, nothing on the console and the admin, whose slices load as pages show them.
    const preloads = [...readFileSync(join(dist, "index.html"), "utf8").matchAll(/<link\b[^>]*\brel="preload"[^>]*>/g)]
      .filter(([tag]) => /\bas="font"/.test(tag))
      .map(([tag]) => /\bhref="([^"]+)"/.exec(tag)?.[1]?.slice(base.length) ?? "");
    const marked = fonts.faces
      .flatMap((face) => face.files)
      .filter((file) => file.preload?.some((page) => page.split("/")[0] === app));
    for (const href of preloads.filter((href) => !marked.some((file) => href.startsWith(`assets/${stem(file)}`)))) {
      failures.push(`${app}: the entry page preloads ${href}, which fonts.json does not mark for preloading`);
    }
    for (const file of marked.filter((file) => !preloads.some((href) => href.startsWith(`assets/${stem(file)}`)))) {
      failures.push(`${app}: the entry page does not preload ${file.file}, which fonts.json marks for preloading`);
    }
    const preloaded = preloads.reduce((total, file) => total + readFileSync(join(dist, file)).length, 0);
    console.log(`${`${app}: first screen font preload`.padEnd(36)} ${(preloaded / kB).toFixed(1).padStart(6)} kB`);
  }

  const font = fontBudget(fonts);
  console.log(font.lines.join("\n"));
  failures.push(...font.failures);

  if (failures.length > 0) {
    console.error(`\nover budget:\n${failures.join("\n")}`);
    process.exit(1);
  }
}
