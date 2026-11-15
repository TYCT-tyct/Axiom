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
      name: "Chinese over 100 kB in every file of the face a page is set in, though its own text needs fewer",
      faces: [
        face("han", [
          ["shell", ["console/pages/keys", "console/pages/calls"], 90_000],
          ["keys", ["console/pages/keys"], 5_000],
          ["calls", ["console/pages/calls"], 5_001],
        ]),
      ],
      failures: [
        "console/pages/calls: Chinese fonts",
        "  shell 90.0 kB",
        "  keys 5.0 kB",
        "  calls 5.0 kB",
        "console/pages/keys: Chinese fonts",
        "  shell 90.0 kB",
        "  keys 5.0 kB",
        "  calls 5.0 kB",
      ],
    },
    {
      name: "another app's face, which its pages are not set in",
      faces: [
        face("han", [["console", ["console/pages/keys"], 99_000]]),
        face("han", [["admin", ["admin/pages/users"], 99_000]]),
      ],
      failures: [],
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
