import { createRoute, lazyRouteComponent } from "@tanstack/react-router";
import {
  type KeyFilters,
  keyFiltersOf,
  keysQuery,
  providerKeysQuery,
  requestPresetsQuery,
  routingDefaultQuery,
  routingPresetsQuery,
  workspacesQuery,
} from "../queries";
import { ask, askingWith, warming } from "../warm";
import { app, text } from "./shell";

// The API keys, the workspaces, the presets and the provider keys.
export const keysRoutes = [
  createRoute({
    getParentRoute: () => app,
    path: "/keys",
    validateSearch: (search: Record<string, unknown>) => ({
      name: text(search.name),
      prefix: text(search.prefix),
      status: keyStatus(search.status),
      created_after: text(search.created_after),
      created_before: text(search.created_before),
      workspace_id: text(search.workspace_id),
    }),
    loaderDeps: ({ search }) => keyFiltersOf(search),
    loader: warming(async () => (client, _session, filters: KeyFilters) => ask(client, [keysQuery(filters)])),
    component: lazyRouteComponent(() => import("../pages/keys")),
  }),
  createRoute({
    getParentRoute: () => app,
    path: "/workspaces",
    loader: askingWith("api_keys.view", workspacesQuery),
    component: lazyRouteComponent(() => import("../pages/workspaces")),
  }),
  createRoute({
    getParentRoute: () => app,
    path: "/routing-presets",
    loader: askingWith("api_keys.view", routingPresetsQuery, routingDefaultQuery),
    component: lazyRouteComponent(() => import("../pages/routing-presets")),
  }),
  createRoute({
    getParentRoute: () => app,
    path: "/request-presets",
    loader: askingWith("api_keys.view", requestPresetsQuery),
    component: lazyRouteComponent(() => import("../pages/request-presets")),
  }),
  createRoute({
    getParentRoute: () => app,
    path: "/provider-keys",
    loader: askingWith("api_keys.view", providerKeysQuery),
    component: lazyRouteComponent(() => import("../pages/provider-keys")),
  }),
] as const;

// A status from the address is one the API knows; anything else is no filter, not a request it would refuse.
function keyStatus(value: unknown): KeyFilters["status"] {
  return value === "active" || value === "disabled" || value === "expired" ? value : undefined;
}
