// Use the same content keys and relative archive paths for local and Actions caches.
import { createRequire } from "node:module";
import { rmSync } from "node:fs";

// The toolkit's tar base uses GITHUB_WORKSPACE, not cwd; keep archives portable between cache roots.
process.env.GITHUB_WORKSPACE = process.cwd();
const cache = createRequire(import.meta.url)(process.env.ACTIONS_CACHE_CLIENT);
const [action, key] = process.argv.slice(2);
try {
  if (action === "restore") {
    // The toolkit suppresses extraction errors and returns undefined, potentially leaving partial files.
    if (await cache.restoreCache([key], key) !== key) rmSync(key, { recursive: true, force: true });
  } else {
    await cache.saveCache([key], key);
  }
} catch (error) {
  // Cache outages must run the real check, never turn a failed check into a pass.
  console.log(`::warning::Actions cache ${action}: ${error.message}`);
  process.exitCode = 1;
}
