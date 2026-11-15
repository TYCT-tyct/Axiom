// The per-page font budget (docs/frontend.md「前端性能指标」): the font files a page can load, each counted once, at
// most 60 kB for Latin text and digits and, on the website, 100 kB for Chinese. The console and the admin set Chinese
// in the system's font, so their Chinese budget is zero. Which files a page can load is fonts.json, which the font
// pipeline (scripts/fonts/build.py) writes: a face's files carry the pages that show their characters.

const kB = 1000;

type FontFile = { file: string; pages: string[]; bytes: number };
export type FontManifest = { faces: { family: string; weight: string; script: "latin" | "han"; files: FontFile[] }[] };

const budgets = (page: string) => ({ latin: 60 * kB, han: page.startsWith("site/") ? 100 * kB : 0 });
const scriptNames = { latin: "Latin", han: "Chinese" } as const;

// pageFiles returns the files of one script a page can load, each once, by name.
function pageFiles(manifest: FontManifest, page: string, script: "latin" | "han") {
  const files = new Map<string, number>();
  for (const face of manifest.faces.filter((f) => f.script === script)) {
    for (const file of face.files.filter((f) => f.pages.includes(page))) {
      files.set(file.file, file.bytes);
    }
  }
  return files;
}

// fontBudget returns a line per page and script, and the lines over budget, each followed by the files it adds up.
export function fontBudget(manifest: FontManifest) {
  const lines: string[] = [];
  const failures: string[] = [];
  const pages = [...new Set(manifest.faces.flatMap((face) => face.files.flatMap((file) => file.pages)))].sort();
  for (const page of pages) {
    for (const script of ["latin", "han"] as const) {
      const files = pageFiles(manifest, page, script);
      if (files.size === 0) {
        continue;
      }
      const total = [...files.values()].reduce((sum, bytes) => sum + bytes, 0);
      const budget = budgets(page)[script];
      const line = `${`${page}: ${scriptNames[script]} fonts`.padEnd(36)} ${(total / kB).toFixed(1).padStart(6)} kB of ${budget / kB} kB`;
      lines.push(line);
      if (total > budget) {
        failures.push(line, ...[...files].map(([file, bytes]) => `  ${file} ${(bytes / kB).toFixed(1)} kB`));
      }
    }
  }
  return { lines, failures };
}
