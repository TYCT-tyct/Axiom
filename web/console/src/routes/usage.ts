import { createRoute, lazyRouteComponent } from "@tanstack/react-router";
import type { PresetChoice } from "../pages/calls-data";
import type { UsageSearch } from "../pages/usage-handlers";
import type { CallFilters, CallGroupBy } from "../queries";
import { warming } from "../warm";
import { app, text } from "./shell";

// The overview, the usage and the calls.
export const usageRoutes = [
  createRoute({
    getParentRoute: () => app,
    path: "/",
    loader: warming(() => import("../pages/overview").then((page) => page.warm)),
    component: lazyRouteComponent(() => import("../pages/overview")),
  }),
  createRoute({
    getParentRoute: () => app,
    path: "/usage",
    validateSearch: (search: Record<string, unknown>) => ({
      range: usageRange(search.range),
      since: text(search.since),
      until: text(search.until),
      granularity: usageGranularity(search.granularity),
      metric: usageMetric(search.metric),
      group_by: usageGroupBy(search.group_by),
      currency: text(search.currency),
      model: text(search.model),
      key_id: text(search.key_id),
      workspace_id: text(search.workspace_id),
    }),
    loaderDeps: ({ search }) => search,
    loader: warming(() => import("../pages/usage").then((page) => page.warm)),
    component: lazyRouteComponent(() => import("../pages/usage")),
  }),
  createRoute({
    getParentRoute: () => app,
    path: "/calls",
    validateSearch: (search: Record<string, unknown>) => ({
      since: text(search.since),
      until: text(search.until),
      preset: callRangePreset(search.preset),
      model: text(search.model),
      client_app: text(search.client_app),
      key_id: text(search.key_id),
      workspace_id: text(search.workspace_id),
      status: callStatus(search.status),
      fell_back:
        search.fell_back === true || search.fell_back === "true" || search.fell_back === "1" ? true : undefined,
      request_id: text(search.request_id),
      group_by: callGroupBy(search.group_by),
      tab: search.tab === "sessions" ? ("sessions" as const) : undefined,
      call: text(search.call),
    }),
    loaderDeps: ({ search }) => search,
    loader: warming(() => import("../pages/calls").then((page) => page.warm)),
    component: lazyRouteComponent(() => import("../pages/calls")),
  }),
  createRoute({
    getParentRoute: () => app,
    path: "/calls/$callId",
    component: lazyRouteComponent(() => import("../pages/call")),
  }),
] as const;

// A result or grouping from the address is one the API knows; anything else is no filter, not a request it would refuse.
function callStatus(value: unknown): CallFilters["status"] {
  return value === "success" || value === "error" ? value : undefined;
}

function callGroupBy(value: unknown): CallGroupBy | undefined {
  return value === "day" || value === "model" || value === "key" ? value : undefined;
}

function callRangePreset(value: unknown): PresetChoice | undefined {
  return value === "today" || value === "d7" || value === "d30" || value === "month" ? value : undefined;
}

function usageRange(value: unknown): UsageSearch["range"] {
  return value === "today" || value === "d7" || value === "d30" || value === "month" || value === "custom"
    ? value
    : undefined;
}

function usageGranularity(value: unknown): UsageSearch["granularity"] {
  return value === "hour" || value === "day" || value === "week" ? value : undefined;
}

function usageMetric(value: unknown): UsageSearch["metric"] {
  return value === "spend" || value === "requests" || value === "tokens" ? value : undefined;
}

function usageGroupBy(value: unknown): UsageSearch["group_by"] {
  return value === "model" || value === "key" || value === "workspace" ? value : undefined;
}
