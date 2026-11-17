import { execFileSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, expect, it, vi } from "vitest";

vi.mock("node:fs", async (original) => {
  const fs = await original<typeof import("node:fs")>();
  return { ...fs, writeFileSync: vi.fn(fs.writeFileSync) };
});

const archive = execFileSync("python3", [
  "-c",
  "import io,sys,zipfile; b=io.BytesIO(); z=zipfile.ZipFile(b,'w'); z.writestr('font.ttf',b'verified font'); z.close(); sys.stdout.buffer.write(b.getvalue())",
]);
const sha256 = createHash("sha256").update(archive).digest("hex");
const name = `font-test-${randomUUID()}`;
const local = join(import.meta.dirname, "../packages/boards/node_modules/.cache/boards-fonts", `${sha256}-${name}`);
let directory: string;

async function loader() {
  vi.resetModules();
  const fonts = await import("../packages/boards/fonts.ts");
  fonts.sources[name] = { url: "https://example.test/font.zip", sha256, member: "font.ttf" };
  return fonts;
}

function setup() {
  directory = mkdtempSync(join(tmpdir(), "axiom-boards-fonts-"));
  vi.stubEnv("XDG_CACHE_HOME", directory);
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(archive)),
  );
  const shared = join(directory, "axiom-fonts", `${sha256}-${name}`);
  mkdirSync(join(directory, "axiom-fonts"));
  return shared;
}

afterEach(() => {
  vi.mocked(writeFileSync).mockRestore();
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
  rmSync(directory, { recursive: true, force: true });
  rmSync(local, { force: true });
});

it("extracts the verified shared archive even when an old package cache contains a partial copy", async () => {
  const shared = setup();
  writeFileSync(shared, archive);
  mkdirSync(join(local, ".."), { recursive: true });
  writeFileSync(local, archive.subarray(0, 8));
  const fonts = await loader();
  await expect(fonts.sourceFont(name)).resolves.toEqual(Buffer.from("verified font"));
  expect(fetch).not.toHaveBeenCalled();
});

it("lets independent loaders read during cache publication without observing partial bytes", async () => {
  const shared = setup();
  const writer = await loader();
  const reader = await loader();
  const actualWrite = vi.mocked(writeFileSync).getMockImplementation();
  let reading: Promise<Buffer> | undefined;
  vi.mocked(writeFileSync).mockImplementation((...args) => {
    if (!reading && String(args[0]).includes(name)) {
      actualWrite?.(args[0], archive.subarray(0, 8));
      reading = reader.sourceFont(name);
    }
    return actualWrite?.(...args);
  });
  await expect(writer.sourceFont(name)).resolves.toEqual(Buffer.from("verified font"));
  expect(reading).toBeDefined();
  await expect(reading).resolves.toEqual(Buffer.from("verified font"));
  expect(readFileSync(shared)).toEqual(archive);
});

it("rejects a corrupt shared archive before extracting or serving it", async () => {
  const shared = setup();
  writeFileSync(shared, archive.subarray(0, 8));
  const fonts = await loader();
  await expect(fonts.sourceFont(name)).rejects.toThrow(`font digest mismatch: ${name}`);
  expect(fetch).not.toHaveBeenCalled();
});
