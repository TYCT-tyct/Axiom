// Copies the three built apps into the Go tree for the program to embed (internal/platform/webui), with a brotli and a
// gzip copy beside each text file the copy is smaller for, and the content security policy the server sends. Run after
// `pnpm build`, by `just web-embed`.
import { cp, mkdir, readdir, readFile, rm, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { brotli, gzip } from "./compress.ts";

const apps = ["site", "console", "admin"];
// What the server compresses: text. Fonts and images are compressed already.
const text = /\.(html|js|css|svg|json|txt|webmanifest)$/;
const policyFile = "content-security-policy.txt";

async function files(dir: string): Promise<string[]> {
  const entries = await readdir(dir, { withFileTypes: true });
  const nested = await Promise.all(
    entries.map((entry) => (entry.isDirectory() ? files(join(dir, entry.name)) : [join(dir, entry.name)])),
  );
  return nested.flat();
}

// embed replaces the apps and the policy in target with the apps built under web, and leaves anything else there.
export async function embed(web: string, target: string, policy: string) {
  for (const app of apps) {
    const into = join(target, app);
    await rm(into, { recursive: true, force: true });
    // The manifest is for the budget check; the program has no use for it.
    await cp(join(web, app, "dist"), into, { recursive: true, filter: (from) => !from.endsWith(".vite") });
    for (const file of (await files(into)).filter((name) => text.test(name))) {
      const data = await readFile(file);
      for (const [suffix, compressed] of [
        [".br", brotli(data)],
        [".gz", gzip(data)],
      ] as const) {
        if (compressed.length < data.length) {
          await writeFile(file + suffix, compressed);
        }
      }
    }
  }
  await mkdir(target, { recursive: true });
  await writeFile(join(target, policyFile), `${policy}\n`);
}

if (import.meta.main) {
  const { contentSecurityPolicy } = await import("../vite.shared.ts");
  const web = join(import.meta.dirname, "..");
  const target = join(web, "..", "internal", "platform", "webui", "build");
  await embed(web, target, contentSecurityPolicy);
  const built = await Promise.all(apps.map((app) => stat(join(target, app, "index.html"))));
  console.log(`embedded ${built.length} apps into ${target}`);
}
