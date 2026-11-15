import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { faceTexts, readManifest } from "./text.ts";

// The committed fonts still match the translation files and sources.json: a character the copy gained since the fonts
// were cut would be drawn in a fallback font, so the check fails and says to run the pipeline again. Characters a
// source font lacks altogether are listed as absent by the pipeline and fall back by design.

const rerun = "run `pnpm --dir web fonts` and commit its output";
const dir = join(import.meta.dirname, "../../packages/ui/src/css/fonts");
type Built = { family: string; weight: string; style: string; pages: string[]; absent: string };
type BuiltFace = Built & { files: { file: string; pages: string[]; sha256: string; chars: string }[] };
const built: { faces: BuiltFace[] } = JSON.parse(readFileSync(join(dir, "fonts.json"), "utf8"));
const manifest = readManifest();
const texts = await faceTexts(manifest);

describe("the committed fonts", () => {
  it(`were cut from the current sources.json (${rerun})`, () => {
    const key = ({ family, weight, style, pages }: Omit<Built, "absent">) => ({ family, weight, style, pages });
    expect(built.faces.map(key)).toEqual(manifest.faces.map((face) => key({ ...face, style: face.style ?? "normal" })));
  });

  it(`cover every character the pages show (${rerun})`, () => {
    const missing = manifest.faces.flatMap((face, i) =>
      Object.entries(texts[i] ?? {}).flatMap(([page, chars]) => {
        const out = built.faces[i];
        const covered = new Set(
          (out?.absent ?? "") +
            (out?.files ?? [])
              .filter((f) => f.pages.includes(page))
              .map((f) => f.chars)
              .join(""),
        );
        const lacking = [...chars].filter((c) => !covered.has(c)).join("");
        return lacking ? [`${page} ${face.family} ${face.weight}: ${lacking}`] : [];
      }),
    );
    expect(missing).toEqual([]);
  });

  it(`are the files fonts.json describes (${rerun})`, () => {
    for (const file of built.faces.flatMap((face) => face.files)) {
      const sha256 = createHash("sha256")
        .update(readFileSync(join(dir, file.file)))
        .digest("hex");
      expect(sha256, file.file).toBe(file.sha256);
    }
  });
});
