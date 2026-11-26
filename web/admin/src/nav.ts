import type { GlyphName } from "@axiom/ui";
import type shell from "./locales/zh/shell";
import { people } from "./nav/people";
import { records } from "./nav/records";
import { run } from "./nav/run";
import { settingsGroups } from "./nav/settings";
import { supply } from "./nav/supply";

// The admin's navigation as the admin board draws it (adNavDef, adSetDef and adPhoneTabs in admin_core.js; the table in
// ADMIN_HOWTO「导航怎么分」): four named groups in the sidebar, and the system settings, whose pages replace the
// sidebar with their own five groups. Each design page points at the route that serves it today; the routes the design
// folds into a page's tabs belong to that page's entry (also), which they mark as current and under which ⌘K finds
// them, until the page's own tabs link them. An entry shows only if the session holds its permission, and a group with
// no entry left is hidden. The server checks the permission again on every request. Each group keeps its entries in a
// file of its own under nav/, and the settings theirs in nav/settings.ts; this file sets the groups' order.

// What waits on a page, counted from the queries the shell already has (pending.ts).
export type Pending = "alerts" | "approvals" | "payments" | "inbox";

// label names the route in ⌘K (shell.also); a route without one is a detail page, found from its list.
type Also = { to: string; label?: keyof typeof shell.also };
export type NavEntry = {
  key: keyof typeof shell.nav;
  to: string;
  icon: GlyphName;
  permission: string;
  pending?: Pending;
  also?: readonly Also[];
};
export type Group<K> = { key: K; entries: readonly NavEntry[] };

export const groups: readonly Group<keyof typeof shell.groups>[] = [
  { key: "run", entries: run },
  { key: "people", entries: people },
  { key: "supply", entries: supply },
  { key: "records", entries: records },
];

export { settingsEntry, settingsGroups, settingsRoutes } from "./nav/settings";

// Every page the navigation reaches, for the first page a session may open (pages/home.tsx).
export const pages = [...groups, ...settingsGroups].flatMap((group) => group.entries);

// The phone's bottom entries: the first four of these the session may open (adPhoneTabs), then 更多.
export const phoneTabs = [
  "overview",
  "ops",
  "approvals",
  "users",
  "calls",
] as const satisfies readonly NavEntry["key"][];
