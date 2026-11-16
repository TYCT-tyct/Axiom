import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

type Source = { url: string; sha256: string; member?: string };
export const sources: Record<string, Source> = JSON.parse(
  readFileSync(new URL("../../scripts/fonts/sources.json", import.meta.url), "utf8"),
).sources;
const cache = join(import.meta.dirname, "node_modules", ".cache", "boards-fonts");

// Reuse F1a's pinned sources and existing download cache. Only this package's ignored cache is written.
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
  const local = join(cache, file);
  let data: Buffer;
  if (existsSync(shared)) data = readFileSync(shared);
  else if (existsSync(local)) data = readFileSync(local);
  else {
    const response = await fetch(source.url);
    if (!response.ok) throw new Error(`font download failed: ${name} ${response.status}`);
    data = Buffer.from(await response.arrayBuffer());
  }
  if (createHash("sha256").update(data).digest("hex") !== source.sha256)
    throw new Error(`font digest mismatch: ${name}`);
  mkdirSync(cache, { recursive: true });
  if (!existsSync(local)) writeFileSync(local, data);
  if (!source.member) return data;
  return execFileSync(
    "python3",
    [
      "-c",
      "import sys,zipfile; sys.stdout.buffer.write(zipfile.ZipFile(sys.argv[1], metadata_encoding='utf-8').read(sys.argv[2]))",
      local,
      source.member,
    ],
    { maxBuffer: 64 * 1024 * 1024 },
  );
}
