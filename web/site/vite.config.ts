import { appConfig, ports } from "../vite.shared.ts";

// The console shares the website's listener under /console/ (console/vite.config.ts).
export default appConfig({ port: ports.site, mounts: { "/console": ports.console } });
