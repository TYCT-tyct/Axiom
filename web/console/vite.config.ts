import { appConfig } from "../vite.shared.ts";

// The console shares the public listener with the website, under its own path.
export default appConfig({ port: 5174, base: "/console/" });
