import { randomBytes } from "node:crypto";
import { mkdir, mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { brotliDecompressSync, gunzipSync } from "node:zlib";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { embed } from "./embed.ts";

let root: string;
beforeEach(async () => {
  root = await mkdtemp(join(tmpdir(), "axiom-embed-"));
});
afterEach(() => rm(root, { recursive: true, force: true }));

// Random bytes do not shrink, whatever the algorithm.
const noise = randomBytes(64);
const script = "console.log('hello embedded world');\n".repeat(200);

async function build(app: string, files: Record<string, string | Buffer>) {
  for (const [name, content] of Object.entries(files)) {
    const path = join(root, "web", app, "dist", name);
    await mkdir(join(path, ".."), { recursive: true });
    await writeFile(path, content);
  }
}

async function names(dir: string): Promise<string[]> {
  return (await readdir(dir, { recursive: true, withFileTypes: true }))
    .filter((entry) => entry.isFile())
    .map((entry) =>
      join(entry.parentPath, entry.name)
        .slice(dir.length + 1)
        .replaceAll("\\", "/"),
    )
    .sort();
}

describe("embed", () => {
  it("copies each app, compresses its text files into copies that decompress to them, and writes the policy", async () => {
    for (const app of ["site", "console", "admin"]) {
      await build(app, {
        "index.html": noise,
        "assets/index-AAAAAAAA.js": script,
        "assets/inter-BBBBBBBB.woff2": Buffer.from("not text"),
        ".vite/manifest.json": "{}",
      });
    }
    const target = join(root, "target");
    await embed(join(root, "web"), target, "default-src 'self'");

    expect(await readFile(join(target, "content-security-policy.txt"), "utf8")).toBe("default-src 'self'\n");
    // The page does not shrink, the font is not text, and the manifest is not shipped.
    expect(await names(join(target, "console"))).toEqual([
      "assets/index-AAAAAAAA.js",
      "assets/index-AAAAAAAA.js.br",
      "assets/index-AAAAAAAA.js.gz",
      "assets/inter-BBBBBBBB.woff2",
      "index.html",
    ]);
    const asset = join(target, "console", "assets", "index-AAAAAAAA.js");
    expect(brotliDecompressSync(await readFile(`${asset}.br`)).toString()).toBe(script);
    expect(gunzipSync(await readFile(`${asset}.gz`)).toString()).toBe(script);
  });

  it("replaces what an earlier build left, and nothing else", async () => {
    const target = join(root, "target");
    await mkdir(join(target, "site", "assets"), { recursive: true });
    await writeFile(join(target, "site", "assets", "old-CCCCCCCC.js"), "old");
    await writeFile(join(target, "NOT_BUILT.txt"), "kept");
    for (const app of ["site", "console", "admin"]) {
      await build(app, { "index.html": noise });
    }
    await embed(join(root, "web"), target, "p");
    expect(await names(join(target, "site"))).toEqual(["index.html"]);
    expect(await readFile(join(target, "NOT_BUILT.txt"), "utf8")).toBe("kept");
  });
});
