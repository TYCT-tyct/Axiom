import type { AdminSchemas } from "@axiom/api";
import { useState } from "react";

// The window a ledger list opens with when the address has none: the dashboard always supplies one, while a direct
// visit gets the last day. The anchor is fixed for the visit so paging does not move the window.
export function useListWindow(since?: string, until?: string) {
  const [anchor] = useState(() => new Date());
  return {
    since: since ?? new Date(anchor.getTime() - 24 * 60 * 60 * 1000).toISOString(),
    until: until ?? anchor.toISOString(),
  };
}

export type LedgerListProps = { siteId: AdminSchemas["SiteId"]; since?: string; until?: string };
