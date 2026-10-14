import { appConfig, ports } from "../vite.shared.ts";

// The console shares the public listener with the website, under its own path.
export default appConfig({ port: ports.console, base: "/console/" });
