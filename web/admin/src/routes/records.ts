import { loadTranslatedPage } from "../locales/load-page";
import { callStatus } from "../overview";
import { callPlugin } from "../plugins";
import { shellPage, text } from "./shell";

// 内容与记录: the announcements, the calls and the audit log.
export const recordsRoutes = [
  shellPage("/announcements", () => loadTranslatedPage(import("../pages/announcements"))),
  shellPage(
    "/calls",
    () => loadTranslatedPage(import("../pages/calls")),
    (search) => ({
      account_id: text(search.account_id),
      request_id: text(search.request_id),
      site_id: text(search.site_id),
      model: text(search.model),
      status: callStatus(search.status),
      plugin: callPlugin(search.plugin),
      since: text(search.since),
      until: text(search.until),
    }),
  ),
  shellPage(
    "/audit",
    () => loadTranslatedPage(import("../pages/audit")),
    (search) => ({
      since: text(search.since),
      until: text(search.until),
      action: text(search.action),
      module: text(search.module),
      actor_kind: text(search.actor_kind),
      actor_id: text(search.actor_id),
      target_kind: text(search.target_kind),
      target_id: text(search.target_id),
      account_id: text(search.account_id),
      site_id: text(search.site_id),
      request_id: text(search.request_id),
    }),
  ),
] as const;
