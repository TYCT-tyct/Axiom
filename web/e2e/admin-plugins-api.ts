import type { AdminState } from "./admin-data.ts";
import { nano, type Schemas } from "./admin-fixtures.ts";
import { base, type Ctx, reads, route } from "./admin-route.ts";
import { ok } from "./mock.ts";

// The plugins' side of the admin: which calls used one (every fourth searched the web, every twelfth also read a file and
// that run failed), and how the plugins were used over a window, counted from those calls.

function callPlugins(n: number): Pick<Schemas["AdminCallSummary"], "plugins"> {
  if (n % 4 !== 0) return {};
  const web = { plugin: "web" as const, ok: true, engine: "searxng", uses: 1, ms: 420, price: nano("2000000") };
  const file = { plugin: "file-parser" as const, ok: false, engine: "ocr", uses: 3, ms: 900 };
  return { plugins: n % 12 === 0 ? [web, file] : [web] };
}

// The calls the state starts with, given their plugins; a call's trace shares its summary, so it has them too.
export function startPlugins(state: AdminState) {
  for (const [index, call] of state.calls.entries()) Object.assign(call, callPlugins(index + 1));
}

function getPluginStats({ query, state }: Ctx<"/api/admin/v1/reports/plugins", "get">) {
  const until = new Date(query.get("until") ?? "2026-10-30T23:59:59.000Z");
  const hours = Array.from({ length: 24 }, (_, hour) => new Date(until.getTime() - (23 - hour) * 3_600_000));
  const stat = (plugin: Schemas["AdminPlugin"]) => {
    const runs = state.calls.flatMap((call) => call.plugins?.filter((run) => run.plugin === plugin) ?? []);
    const total = runs.reduce((sum, run) => sum + (run.ms ?? 0), 0);
    return {
      plugin,
      calls: runs.length,
      failures: runs.filter((run) => !run.ok).length,
      uses: runs.reduce((sum, run) => sum + (run.uses ?? 0), 0),
      ...(runs.length ? { mean_ms: Math.round(total / runs.length) } : {}),
      hours: hours.map((at, hour) => ({ at: at.toISOString(), calls: hour === 23 ? runs.length : 0 })),
    };
  };
  return ok({
    since: query.get("since") ?? hours[0]?.toISOString() ?? "",
    until: until.toISOString(),
    plugins: [stat("web"), stat("file-parser")],
  });
}

const engine = {
  id: "00000000-0000-4000-8000-000000000001",
  name: "searxng-main",
  kind: "searxng" as const,
  url: "https://search.example.com",
  has_key: false,
  enabled: true,
  default: true,
  cost_per_search: "0.001",
  cost_per_extra_result: "0",
  markup: "2",
  price_per_search: "0.002",
  price_per_extra_result: "0",
  price_version: 1,
};

// The two settings the plugins page hosts, as the settings page read them.
export const pluginHandlers = [
  route("get", `${base}/reports/plugins`, reads("sites.manage"), getPluginStats),
  route("get", `${base}/settings/search`, reads("sites.manage"), () =>
    ok({ engines: [engine], source: "database" as const }),
  ),
  route("get", `${base}/settings/file-parser`, reads("sites.manage"), () =>
    ok({ url: "https://parser.example.com", has_key: true, source: "database" }),
  ),
];
