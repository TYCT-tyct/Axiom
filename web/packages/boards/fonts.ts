import { execFileSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";

type Source = { url: string; sha256: string; member?: string };
export const sources: Record<string, Source> = JSON.parse(
  readFileSync(new URL("../../scripts/fonts/sources.json", import.meta.url), "utf8"),
).sources;

// Reuse F1a's pinned sources and its shared download cache.
const loaded = new Map<string, Promise<Buffer>>();
export function sourceFont(name: string): Promise<Buffer> {
  let result = loaded.get(name);
  if (!result) {
    result = loadFont(name);
    loaded.set(name, result);
  }
  return result;
}

async function loadFont(name: string): Promise<Buffer> {
  const source = sources[name];
  if (!source) throw new Error(`unknown font ${name}`);
  const file = `${source.sha256}-${name}`;
  const shared = join(process.env.XDG_CACHE_HOME || join(homedir(), ".cache"), "axiom-fonts", file);
  let data: Buffer;
  if (existsSync(shared)) data = readFileSync(shared);
  else {
    const response = await fetch(source.url);
    if (!response.ok) throw new Error(`font download failed: ${name} ${response.status}`);
    data = Buffer.from(await response.arrayBuffer());
  }
  if (createHash("sha256").update(data).digest("hex") !== source.sha256)
    throw new Error(`font digest mismatch: ${name}`);
  // The shared cache outlives the checkout (CI keeps it on its web cache volume): a font is downloaded once, not on
  // every run. Written under a temporary name and renamed, so a concurrent reader never sees half a file.
  if (!existsSync(shared)) {
    mkdirSync(dirname(shared), { recursive: true });
    const partial = `${shared}.${randomUUID()}.part`;
    try {
      writeFileSync(partial, data);
      renameSync(partial, shared);
    } finally {
      rmSync(partial, { force: true });
    }
  }
  if (!source.member) return data;
  return execFileSync(
    "python3",
    [
      "-c",
      "import io,sys,zipfile; sys.stdout.buffer.write(zipfile.ZipFile(io.BytesIO(sys.stdin.buffer.read()), metadata_encoding='utf-8').read(sys.argv[1]))",
      source.member,
    ],
    { input: data, maxBuffer: 64 * 1024 * 1024 },
  );
}
