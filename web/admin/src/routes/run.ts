import { approvalState } from "../approvals";
import { loadTranslatedPage } from "../locales/load-page";
import { overviewRange } from "../overview";
import { shellPage } from "./shell";

// 运行: the dashboard, the operations monitor and its alerts, and the approvals.
export const runRoutes = [
  shellPage(
    "/",
    () => loadTranslatedPage(import("../pages/home")),
    (search) => ({ range: overviewRange(search.range) }),
  ),
  shellPage(
    "/monitor",
    () => loadTranslatedPage(import("../pages/monitor")),
    (search) => ({ window: search.window === "24h" ? ("24h" as const) : ("1h" as const) }),
  ),
  shellPage("/alert-events", () => loadTranslatedPage(import("../pages/alert-events"))),
  shellPage(
    "/approvals",
    () => import("../pages/approvals"),
    (search) => ({
      state: approvalState(search.state),
      view: search.view === "done" ? ("done" as const) : undefined,
    }),
  ),
  shellPage("/approvals/$approvalId", () => import("../pages/approval")),
] as const;
