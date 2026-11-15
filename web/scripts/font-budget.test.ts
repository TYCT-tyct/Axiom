import { describe, expect, it } from "vitest";
import { type FontManifest, fontBudget } from "./font-budget.ts";

const face = (script: "latin" | "han", files: [string, string[], number][]): FontManifest["faces"][number] => ({
  family: "F",
  weight: "400",
  script,
  files: files.map(([file, pages, bytes]) => ({ file, pages, bytes })),
});

describe("fontBudget", () => {
  it.each([
    {
      name: "a site page within both budgets",
      faces: [face("latin", [["sans", ["site/home"], 40_000]]), face("han", [["cjk", ["site/home"], 99_000]])],
      failures: [],
    },
    {
      name: "Latin over 60 kB, with each file listed",
      faces: [face("latin", [["sans", ["console"], 40_000]]), face("latin", [["mono", ["console"], 20_001]])],
      failures: ["console: Latin fonts", "  sans 40.0 kB", "  mono 20.0 kB"],
    },
    {
      name: "Chinese over 100 kB on one site page only",
      faces: [
        face("han", [
          ["shared", ["site/home", "site/models"], 60_000],
          ["models", ["site/models"], 40_001],
        ]),
      ],
      failures: ["site/models: Chinese fonts", "  shared 60.0 kB", "  models 40.0 kB"],
    },
    {
      name: "any Chinese font outside the website, which uses the system's",
      faces: [face("han", [["cjk", ["console"], 1]])],
      failures: ["console: Chinese fonts", "  cjk 0.0 kB"],
    },
  ])("$name", ({ faces, failures }) => {
    const result = fontBudget({ faces });
    expect(result.failures.map((line) => line.replace(/\s+[\d.]+ kB of .*$/, ""))).toEqual(failures);
  });

  it("counts a file two of a page's faces name once", () => {
    const shared: [string, string[], number] = ["same", ["admin"], 50_000];
    expect(fontBudget({ faces: [face("latin", [shared]), face("latin", [shared])] }).failures).toEqual([]);
  });
});
