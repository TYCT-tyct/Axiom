import { execFileSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

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
  const cached = existsSync(shared);
  const data = cached ? readFileSync(shared) : await downloadFont(name, source.url);
  if (createHash("sha256").update(data).digest("hex") !== source.sha256)
    throw new Error(`font digest mismatch: ${name}`);
  // The shared cache outlives the checkout (CI keeps it on its web cache volume): a font is downloaded once, not on
  // every run. Written under a temporary name and renamed, so a concurrent reader never sees half a file.
  if (!cached && !existsSync(shared)) {
    mkdirSync(dirname(shared), { recursive: true });
    const partial = `${shared}.${randomUUID()}.part`;
    try {
      writeFileSync(partial, data);
      renameSync(partial, shared);
    } finally {
      rmSync(partial, { force: true });
    }
    console.log(`cached source font: ${name}`);
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

async function downloadFont(name: string, url: string): Promise<Buffer> {
  const maxAttempts = 3;
  const chunks: Uint8Array[] = [];
  let received = 0;
  for (let attempt = 1; ; attempt++) {
    try {
      await readFontResponse(url, received, chunks);
      return Buffer.concat(chunks);
    } catch (error) {
      if (
        attempt === maxAttempts ||
        !(error instanceof TypeError || (error instanceof DOMException && error.name === "TimeoutError"))
      ) {
        throw new Error(`font download failed: ${name}`, { cause: error });
      }
      received = chunks.reduce((total, chunk) => total + chunk.byteLength, 0);
      const waitMs = 1000 * 2 ** (attempt - 1);
      console.warn(
        `font download interrupted: ${name} after ${received} bytes (attempt ${attempt}/${maxAttempts}, retry in ${waitMs} ms): ${error}`,
      );
      await delay(waitMs);
    }
  }
}

async function readFontResponse(url: string, received: number, chunks: Uint8Array[]): Promise<void> {
  const headers = new Headers({ "accept-encoding": "identity" });
  if (received) headers.set("range", `bytes=${received}-`);
  const response = await fetch(url, { headers, signal: AbortSignal.timeout(120_000) });
  if (!response.ok || !response.body) throw new Error(`HTTP ${response.status}: no font body`);
  // A server that ignores Range sends the whole font. The pinned digest verifies the assembled bytes either way.
  if (response.status !== 206) chunks.length = 0;
  const reader = response.body.getReader();
  // Keep each chunk as it arrives so a later read error can resume at the right offset.
  for (let chunk = await reader.read(); !chunk.done; chunk = await reader.read()) {
    chunks.push(chunk.value);
  }
}
