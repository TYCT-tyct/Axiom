import { ports, servedOffset } from "../vite.shared.ts";

const served = (port: number, path = "/") => `http://localhost:${port + servedOffset}${path}`;

// The apps the end-to-end tests load from the Go server (playwright.config.ts starts it): the website and the console on
// its public listener, the admin on its admin listener.
export const apps = [
  { name: "website", url: served(ports.site) },
  { name: "console", url: served(ports.site, "/console/") },
  { name: "admin", url: served(ports.admin) },
] as const;

// An address of the admin app.
export const adminUrl = (path = "") => new URL(path, apps[2].url).href;
