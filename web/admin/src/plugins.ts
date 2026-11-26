import { ApiError } from "@axiom/api";

// The plugins the gateway runs for a request, by the ID a request names them with (api/admin.yaml AdminPlugin).
export const adminPlugins = ["web", "file-parser"] as const;
export type AdminPlugin = (typeof adminPlugins)[number];

// A plugin in the address is one of the two, or nothing.
export function callPlugin(value: unknown): AdminPlugin | undefined {
  return adminPlugins.includes(value as AdminPlugin) ? (value as AdminPlugin) : undefined;
}

// A section whose operation the node has not built yet answers 501: the page leaves the section out, quietly, and it
// shows by itself once the node answers (the contract's operations land before their backends do).
export const notBuilt = (error: unknown) => error instanceof ApiError && error.status === 501;
