// Fails when a built app loads more than its budget (docs/frontend.md「前端性能指标」): the script the first screen
// loads, the script each further page adds, and the font Latin text and digits need. Scripts count at their brotli
// size, the transfer size; fonts are already compressed. Run after `pnpm build`.
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { brotliCompressSync, constants } from "node:zlib";

const kB = 1000;
// firstPage names the lazily loaded page the entry address opens (its manifest key): the first screen loads it too.
const budgets: Record<string, { firstScreen: number; perPage?: number; firstPage?: string }> = {
  site: { firstScreen: 150 * kB },
  console: { firstScreen: 200 * kB, perPage: 50 * kB, firstPage: "src/pages/overview.tsx" },
  admin: { firstScreen: 200 * kB, perPage: 50 * kB },
};
const fontBudget = 60 * kB;
// Fontsource names each file by its Unicode subset; the basic Latin one carries Latin letters and digits.
const latinFont = /-latin-wght-normal-[\w-]+\.woff2$/;

type Chunk = { file: string; isEntry?: boolean; isDynamicEntry?: boolean; imports?: string[] };

function brotliSize(path: string) {
  const compressed = brotliCompressSync(readFileSync(path), { params: { [constants.BROTLI_PARAM_QUALITY]: 11 } });
  return compressed.length;
}

// closure returns the chunk keys a chunk loads before it runs: itself and its static imports, recursively.
function closure(manifest: Record<string, Chunk>, key: string, seen = new Set<string>()) {
  if (!seen.has(key)) {
    seen.add(key);
    for (const imported of manifest[key]?.imports ?? []) {
      closure(manifest, imported, seen);
    }
  }
  return seen;
}

const failures: string[] = [];
function measure(what: string, size: number, budget: number) {
  const line = `${what.padEnd(36)} ${(size / kB).toFixed(1).padStart(6)} kB of ${budget / kB} kB`;
  console.log(line);
  if (size > budget) {
    failures.push(line);
  }
}

for (const [app, budget] of Object.entries(budgets)) {
  const dist = join(import.meta.dirname, "..", app, "dist");
  const manifest: Record<string, Chunk> = JSON.parse(readFileSync(join(dist, ".vite", "manifest.json"), "utf8"));
  const size = (keys: Iterable<string>) =>
    [...keys].reduce((total, key) => total + brotliSize(join(dist, (manifest[key] as Chunk).file)), 0);

  const firstScreen = new Set<string>();
  for (const [key, chunk] of Object.entries(manifest)) {
    if (chunk.isEntry || key === budget.firstPage) {
      closure(manifest, key, firstScreen);
    }
  }
  if (budget.firstPage && !manifest[budget.firstPage]) {
    failures.push(`${app}: the first page ${budget.firstPage} is not in the build; update its name`);
  }
  measure(`${app}: first screen script`, size(firstScreen), budget.firstScreen);

  if (budget.perPage) {
    for (const [key, chunk] of Object.entries(manifest)) {
      if (chunk.isDynamicEntry && key !== budget.firstPage) {
        const added = [...closure(manifest, key)].filter((page) => !firstScreen.has(page));
        measure(`${app}: page ${chunk.file.replace(/^assets\//, "")}`, size(added), budget.perPage);
      }
    }
  }

  const fonts = readdirSync(join(dist, "assets")).filter((file) => latinFont.test(file));
  if (fonts.length === 0) {
    failures.push(`${app}: no Latin font file matches ${latinFont}; update the pattern`);
  }
  const fontSize = fonts.reduce((total, file) => total + readFileSync(join(dist, "assets", file)).length, 0);
  measure(`${app}: Latin font`, fontSize, fontBudget);
}

if (failures.length > 0) {
  console.error(`\nover budget:\n${failures.join("\n")}`);
  process.exit(1);
}
