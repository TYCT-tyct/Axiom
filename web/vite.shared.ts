// The Vite configuration every app shares; an app's vite.config.ts names only its dev server port, the path it is
// served under, and the apps served under paths of its listener.
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { basename, join } from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin, type UserConfig } from "vite";

// The server the end-to-end tests run against (internal/platform/webui/cmd/serveweb) listens this far above an app's dev
// server.
export const servedOffset = 1000;

// firstAppPort reads AXIOM_WEB_PORT: the three apps' dev server ports start here, and their test servers sit
// servedOffset above them, so all six must be ports.
export function firstAppPort(value: string | undefined): number {
  const raw = value ?? "5173";
  const first = /^\d+$/.test(raw) ? Number(raw) : Number.NaN;
  if (!Number.isInteger(first) || first < 1 || first + 2 + servedOffset > 65_535) {
    throw new Error(`AXIOM_WEB_PORT must leave room for three ports and their test server ports, not ${raw}`);
  }
  return first;
}
const firstPort = firstAppPort(process.env.AXIOM_WEB_PORT);
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

// The program from `just up` answers the API, so the dev server talks to a real one: on the public listener, or for the
// admin, which is served only on its own listener, on that one (AXIOM_ADMIN_ADDR's default).
const publicApi = "http://127.0.0.1:8080";

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

// fontPreload links, in the built entry page, the font files fonts.json (scripts/fonts/build.py) marks for preloading on
// this app's pages, the website's first-screen Chinese slice, so they load beside the stylesheet instead of after it has
// been applied. The console and the admin preload nothing: a page's slices load as it shows their characters. An app is
// its directory's name, as fonts.json names pages ("site/home").
function fontPreload(): Plugin {
  let app = "";
  let base = "/";
  return {
    name: "axiom:font-preload",
    apply: "build",
    configResolved(config) {
      app = basename(config.root);
      base = config.base;
    },
    transformIndexHtml(_, { bundle = {} }) {
      const fonts: { faces: { files: { file: string; pages: string[]; preload?: boolean }[] }[] } = JSON.parse(
        readFileSync(join(import.meta.dirname, "packages/ui/src/css/fonts/fonts.json"), "utf8"),
      );
      const stems = fonts.faces
        .flatMap((face) => face.files)
        .filter((file) => file.preload && file.pages.some((page) => page.split("/")[0] === app))
        .map((file) => `${file.file.replace(/\.woff2$/, "")}-`);
      return Object.values(bundle)
        .filter((asset) => asset.type === "asset" && stems.some((stem) => basename(asset.fileName).startsWith(stem)))
        .map((asset) => ({
          tag: "link",
          attrs: { rel: "preload", as: "font", type: "font/woff2", href: base + asset.fileName, crossorigin: true },
          injectTo: "head" as const,
        }));
    },
  };
}

// mounts maps a path to the dev server port of the app production serves there on the same listener, such as the
// console under the website's /console/, so following a link between them works on the dev servers too.
export function appConfig({
  port,
  base = "/",
  mounts = {},
  api = publicApi,
}: {
  port: number;
  base?: string;
  mounts?: Record<string, number>;
  api?: string;
}): UserConfig {
  const forward = Object.fromEntries(
    Object.entries(mounts).map(([path, to]) => [path, { target: `http://localhost:${to}`, ws: true }]),
  );
  return defineConfig({
    base,
    publicDir: false,
    plugins: [react(), tailwindcss(), themeScript(), fontPreload()],
    define: {
      // The version of the program this build ships in, exactly as `axiom version` prints it; `just web-build` sets it.
      // Empty when an app is built or served on its own, which turns the new-version prompt off.
      __AXIOM_VERSION__: JSON.stringify(process.env.AXIOM_VERSION ?? ""),
    },
    build: {
      // Content-hashed names, so a deployment never serves a stale asset under a reused name; the manifest lets the
      // budget check find what the first screen loads.
      manifest: true,
      // A font small enough to inline would become a data: URL, which the policy's font-src 'self' refuses.
      assetsInlineLimit: (file) => (file.endsWith(".woff2") ? false : undefined),
      target: "es2023",
    },
    server: {
      port,
      strictPort: true,
      proxy: { "/api": api, ...forward },
    },
  });
}
