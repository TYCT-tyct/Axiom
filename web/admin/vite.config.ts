import { appConfig, ports } from "../vite.shared.ts";

// The admin is served only on its own listener, never the public one (docs/architecture.md「技术选型」).
export default appConfig({ port: ports.admin });
