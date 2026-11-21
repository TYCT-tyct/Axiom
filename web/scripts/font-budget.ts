// The per-page font budget (docs/frontend.md「前端性能指标」): the font files a page can load, each counted once, at
// most 60 kB for Latin text and digits everywhere, and for Chinese 100 kB on the website and each console page and
// 120 kB on each admin page. Which files a page can load is fonts.json, which the font pipeline (scripts/fonts/build.py) writes: a face's
// files carry the pages whose own text needs them, but a page can load every file of a face it is set in, since text
// from the API (a workspace's name) can hold any character and unicode-range loads the file that has it. The budget is
// that worst case; the line also says what the page's own text loads.

const kB = 1000;

export type FontFile = { file: string; pages: string[]; bytes: number; preload?: string[] };
export type FontManifest = { faces: { family: string; weight: string; script: "latin" | "han"; files: FontFile[] }[] };

// The admin is for the site's operators alone, on its own listener, so its pages may load more Chinese than the website
// and the console, whose first screens are the users' (owner's decision, 2026-11-21, #747).
const budgetOf = (page: string, script: "latin" | "han") =>
  script === "latin" ? 60 * kB : page.split("/")[0] === "admin" ? 120 * kB : 100 * kB;
const scriptNames = { latin: "Latin", han: "Chinese" } as const;

// pageFiles returns the files of one script a page can load, each once, by name: every file of each face whose files
// name the page, or the app the page is of ("console" for "console/auth/sign-in"), whose faces every page of it is
// set in besides its own.
function pageFiles(manifest: FontManifest, page: string, script: "latin" | "han") {
  const files = new Map<string, FontFile>();
  const names = [page, page.split("/")[0]];
  const loads = (face: FontManifest["faces"][number]) =>
    face.script === script && face.files.some((x) => x.pages.some((name) => names.includes(name)));
  for (const face of manifest.faces.filter(loads)) {
    for (const file of face.files) {
      files.set(file.file, file);
    }
  }
  return files;
}

const sum = (files: Iterable<FontFile>) => [...files].reduce((total, file) => total + file.bytes, 0);

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
      const total = sum(files.values());
      const own = sum([...files.values()].filter((file) => file.pages.includes(page)));
      const budget = budgetOf(page, script);
      const line = `${`${page}: ${scriptNames[script]} fonts`.padEnd(44)} ${(total / kB).toFixed(1).padStart(6)} kB of ${budget / kB} kB (its own text ${(own / kB).toFixed(1)} kB)`;
      lines.push(line);
      if (total > budget) {
        failures.push(line, ...[...files.values()].map((file) => `  ${file.file} ${(file.bytes / kB).toFixed(1)} kB`));
      }
    }
  }
  return { lines, failures };
}
