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
type BuiltFace = { absent: string; files: { file: string; pages: string[]; sha256: string; chars: string }[] };
const built: { inputs: string; faces: BuiltFace[] } = JSON.parse(readFileSync(join(dir, "fonts.json"), "utf8"));
const manifest = readManifest();
const texts = await faceTexts(manifest);

// The fingerprint build.py writes into fonts.json: anything in sources.json (a source's hash, an axis, a descriptor, a
// page), the scripts or their pinned tools changes it, and with it what the pipeline would cut.
const inputNames = ["sources.json", "build.py", "text.ts", "routes.ts", "requirements.txt"];
const sha256 = (data: string | Buffer) => createHash("sha256").update(data).digest("hex");
const fingerprint = (read: (name: string) => string | Buffer) =>
  sha256(inputNames.map((name) => `${name} ${sha256(read(name))}\n`).join(""));
const readInput = (name: string) => readFileSync(join(import.meta.dirname, name));

describe("the committed fonts", () => {
  it(`were cut from the current sources.json, scripts and requirements.txt (${rerun})`, () => {
    expect(fingerprint(readInput)).toBe(built.inputs);
  });

  it.each([
    ["a source's SHA-256", /"sha256": "b24f/, '"sha256": "0000'],
    ["an axis", /"wdth": 100/, '"wdth": 90'],
    ["a descriptor", /"size-adjust": "105%"/, '"size-adjust": "110%"'],
  ])("are stale once %s in sources.json changes", (_, from, to) => {
    const edited = readInput("sources.json").toString().replace(from, to);
    expect(edited).not.toBe(readInput("sources.json").toString());
    expect(fingerprint((name) => (name === "sources.json" ? edited : readInput(name)))).not.toBe(built.inputs);
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

  // The screens' words come from the generated regions, not the apps' message files: a heading there (the sea bed's
  // "模型会变，接入不变") must be in the serif face's corpus, which the old corpus, the message files alone, lacked.
  it("count a screen's heading words, which only the boards' regions hold, in the serif face", () => {
    const at = manifest.faces.findIndex(
      (face) => face.family === "AX Serif" && "script" in face.text && face.text.script === "han",
    );
    const serif = texts[at]?.["site/home"] ?? "";
    expect([..."会变接入不变"].filter((c) => !serif.includes(c))).toEqual([]);
  });

  it(`are the files fonts.json describes (${rerun})`, () => {
    for (const file of built.faces.flatMap((face) => face.files)) {
      expect(sha256(readFileSync(join(dir, file.file))), file.file).toBe(file.sha256);
    }
  });
});
