// The Vite configuration every app shares; an app's vite.config.ts names only its dev server port, the path it is
// served under, and the apps served under paths of its listener.
import { join } from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type UserConfig } from "vite";

// An app's preview server listens this far above its dev server.
const previewOffset = 1000;

// The content security policy pages must work under (docs/security.md「网页与域名」): no inline script or style, nothing
// loaded from another origin. The preview server sends it, so the end-to-end tests fail on any output that breaks it.
// This is the policy's one definition: the Go server that serves the builds must send exactly this string.
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

// The program from `just up` answers the API, so the dev and preview servers talk to a real one.
const api = { "/api": "http://127.0.0.1:8080" };

// mounts maps a path to the dev server port of the app production serves there on the same listener, such as the
// console under the website's /console/, so following a link between them works on the dev and preview servers too.
export function appConfig({
  port,
  base = "/",
  mounts = {},
}: {
  port: number;
  base?: string;
  mounts?: Record<string, number>;
}): UserConfig {
  const forward = (offset: number) =>
    Object.fromEntries(
      Object.entries(mounts).map(([path, to]) => [path, { target: `http://localhost:${to + offset}`, ws: true }]),
    );
  return defineConfig({
    base,
    // Files every app serves as they are, at its root: theme.js must run before the bundle, as a classic script.
    publicDir: join(import.meta.dirname, "public"),
    plugins: [react(), tailwindcss()],
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
      proxy: { ...api, ...forward(0) },
    },
    preview: {
      port: port + previewOffset,
      strictPort: true,
      proxy: { ...api, ...forward(previewOffset) },
      headers: { "Content-Security-Policy": contentSecurityPolicy },
    },
  });
}
