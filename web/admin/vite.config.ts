import { appConfig, ports } from "../vite.shared.ts";

// The admin is served only on its own listener, never the public one (docs/architecture.md「技术选型」), and its API is on
// that listener too.
export default appConfig({ port: ports.admin, api: "http://127.0.0.1:8082" });
