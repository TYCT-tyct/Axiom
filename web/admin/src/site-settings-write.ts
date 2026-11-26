import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import type { Context } from "./change";

type View = AdminSchemas["SiteSettingsView"];
type Settings = AdminSchemas["SiteSettings"];

// siteSettingsInput is only the name: the operation keeps whatever registration mode is current when it writes, under
// the site row lock, so a mode another administrator changed while this page was open is not put back. Sending the mode
// read with the page would overwrite that change; a node of the previous release requires the mode, so the page keeps
// the name read-only there instead.
export function siteSettingsInput(name: string): AdminSchemas["SystemSettingsUpdate"]["settings"] {
  return { name };
}

// nameEditable is the capability a node of this release reports; an older node omits it, and the page then leaves the
// name read-only rather than sending a registration mode it read earlier.
export function nameEditable(view: AdminSchemas["SystemSettings"]): boolean {
  return view.name_editable === true;
}

// writeSiteSettings sends a site's own values, which the operation replaces whole (updateSiteSettings). Three pages
// edit them (the site, the sessions, the keys), so each carries the values it does not edit as they are in the view it
// read, and changes only what its own drafts changed: one page's submit never clears another page's setting. A value
// the patch names as undefined goes back to the default in code.
export function writeSiteSettings(siteId: AdminSchemas["SiteId"], view: View, patch: Settings, context: Context) {
  return unwrapChange(
    adminApi.PUT("/api/admin/v1/sites/{site_id}/settings", {
      params: { path: { site_id: siteId }, header: { "Idempotency-Key": context.key } },
      body: { settings: { ...view.settings, ...patch }, reason: context.reason },
    }),
  );
}
