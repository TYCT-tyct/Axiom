// The apps the end-to-end tests load, each from its `vite preview` server (playwright.config.ts starts them).
export const apps = [
  { name: "website", filter: "@axiom/site", url: "http://localhost:6173/" },
  { name: "console", filter: "@axiom/console", url: "http://localhost:6174/console/" },
  { name: "admin", filter: "@axiom/admin", url: "http://localhost:6175/" },
] as const;
