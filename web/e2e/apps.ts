import { ports, previewOffset } from "../vite.shared.ts";

const preview = (port: number, path = "/") => `http://localhost:${port + previewOffset}${path}`;

// The apps the end-to-end tests load, each from its `vite preview` server (playwright.config.ts starts them).
export const apps = [
  { name: "website", filter: "@axiom/site", url: preview(ports.site) },
  { name: "console", filter: "@axiom/console", url: preview(ports.console, "/console/") },
  { name: "admin", filter: "@axiom/admin", url: preview(ports.admin) },
] as const;
