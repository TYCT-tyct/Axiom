// The Vite configuration every app shares; an app's vite.config.ts names only its dev server port, the path it is
// served under, and the apps served under paths of its listener.
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin, type UserConfig } from "vite";

// The server the end-to-end tests run against (internal/platform/webui/cmd/serveweb) listens this far above an app's dev
// server.
export const servedOffset = 1000;

// The apps' dev server ports. AXIOM_WEB_PORT moves them all, and the test servers with them, so another worktree can
// run its servers and end-to-end tests at the same time.
const firstPort = Number(process.env.AXIOM_WEB_PORT ?? 5173);
if (!Number.isInteger(firstPort) || firstPort < 1 || firstPort + 2 + servedOffset > 65_535) {
  throw new Error(`AXIOM_WEB_PORT must leave room for three ports and their test server ports, not ${firstPort}`);
}
export const ports = { site: firstPort, console: firstPort + 1, admin: firstPort + 2 };

// The content security policy pages must work under (docs/security.md「网页与域名」): no inline script or style, nothing
// loaded from another origin. This is the policy's one definition: web/scripts/embed.ts hands it to the Go server
// (internal/platform/webui), which sends exactly this string, so the end-to-end tests fail on any output that breaks it.
export const contentSecurityPolicy = [
  "default-src 'self'",
  "script-src 'self'",
  "style-src 'self'",
  "img-src 'self' data:",
  "font-src 'self'",
  "connect-src 'self'",
  "object-src 'none'",
  "base-uri 'none'",
  "form-action 'self'",
  "frame-ancestors 'none'",
].join("; ");

// The program from `just up` answers the API, so the dev server talks to a real one.
const api = { "/api": "http://127.0.0.1:8080" };

// themeScript serves theme.js to every app and loads it at the start of <head>: a classic script, so it runs before the
// first paint, from the page's own origin, because the policy above forbids inline scripts. Vite bundles only module
// scripts, so the build emits it here, under a name carrying a hash of its content like every other asset, so it can be
// cached long and a new version is never hidden behind the old one's name. The file is read on each use, so an edit
// shows on the dev server without a restart.
function themeScript(): Plugin {
  let base = "/";
  const asset = () => {
    const source = readFileSync(join(import.meta.dirname, "theme.js"), "utf8");
    const hash = createHash("sha256").update(source).digest("base64url").slice(0, 8);
    return { source, fileName: `assets/theme-${hash}.js` };
  };
  return {
    name: "axiom:theme-script",
    configResolved(config) {
      base = config.base;
    },
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const { source, fileName } = asset();
        if (req.url?.split("?")[0] !== base + fileName) {
          return next();
        }
        res.setHeader("Content-Type", "text/javascript");
        res.end(source);
      });
    },
    generateBundle() {
      this.emitFile({ type: "asset", ...asset() });
    },
    transformIndexHtml() {
      return [{ tag: "script", attrs: { src: base + asset().fileName }, injectTo: "head-prepend" }];
    },
  };
}

// mounts maps a path to the dev server port of the app production serves there on the same listener, such as the
// console under the website's /console/, so following a link between them works on the dev servers too.
export function appConfig({
  port,
  base = "/",
  mounts = {},
}: {
  port: number;
  base?: string;
  mounts?: Record<string, number>;
}): UserConfig {
  const forward = Object.fromEntries(
    Object.entries(mounts).map(([path, to]) => [path, { target: `http://localhost:${to}`, ws: true }]),
  );
  return defineConfig({
    base,
    publicDir: false,
    plugins: [react(), tailwindcss(), themeScript()],
    define: {
      // The version of the program this build ships in, exactly as `axiom version` prints it; `just web-build` sets it.
      // Empty when an app is built or served on its own, which turns the new-version prompt off.
      __AXIOM_VERSION__: JSON.stringify(process.env.AXIOM_VERSION ?? ""),
    },
    build: {
      // Content-hashed names, so a deployment never serves a stale asset under a reused name; the manifest lets the
      // budget check find what the first screen loads.
      manifest: true,
      target: "es2023",
    },
    server: {
      port,
      strictPort: true,
      proxy: { ...api, ...forward },
    },
  });
}
