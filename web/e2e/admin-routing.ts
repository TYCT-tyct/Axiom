import type { AdminState } from "./admin-data.ts";
import { long, type Schemas, siteId, uuid } from "./admin-fixtures.ts";
import { base, type Ctx, changes, reads, route, safe } from "./admin-route.ts";
import { fail, type Handler, type MockData, ok } from "./mock.ts";
import { checkOf, withSummary } from "./routing-check.ts";

// The routing presets of the platform and the site (api/admin.yaml, tag Routing), their defaults, the limits on them and
// the platform's switch that suspends them, answered from memory like the rest of the admin's API.

type Preset = Schemas["RoutingPreset"];

type RoutingState = {
  presets: Preset[];
  // The platform's default under "platform", a site's under its ID.
  defaults: Map<string, Schemas["RoutingDefault"]>;
  limits: Schemas["RoutingPresetLimitsView"];
  suspension: Schemas["RoutingPresetSuspension"];
};

// Each mocked node's routing state, kept beside its handlers so the rest of the admin's data stays as it is.
const states = new WeakMap<AdminState, RoutingState>();

// startRouting gives a node its presets: the worst set for the acceptance gate's worst screenshots, the usual otherwise.
export function startRouting(state: AdminState, data: MockData) {
  states.set(state, data === "worst" ? worstRouting() : usualRouting());
}

function routingOf(state: AdminState) {
  const routing = states.get(state) ?? usualRouting();
  states.set(state, routing);
  return routing;
}

const system = "00000000-0000-0000-0000-000000000000";
const at = "2026-11-01T08:00:00Z";
const rules = (value: object) => value as Schemas["RoutingRules"];

function aPreset(n: number, over: Partial<Preset>): Preset {
  return {
    id: uuid(16, n),
    scope: "platform",
    slug: `preset-${n}`,
    name: `Preset ${n}`,
    revision: 1,
    version: 1,
    rules: rules({ default: {} }),
    disabled: false,
    keys: n * 3,
    created_at: at,
    updated_at: at,
    ...over,
  };
}

const defaults: Schemas["RoutingPresetLimits"] = {
  platform: { active: 50, total: 500 },
  site: { active: 50, total: 500 },
  account: { active: 20, total: 200 },
  workspace: { active: 10, total: 100 },
  kept_versions: 50,
  global: { presets: 100_000, bytes: 256 * 1024 * 1024 },
};

// The system preset, a platform preset with run-time settings and one priced in dollars, the site's own, and a disabled
// one; the site's default is its own preset, and the platform raised its kept versions.
function usualRouting(): RoutingState {
  return {
    presets: [
      aPreset(0, { id: system, slug: "allow-all", name: "Allow all", keys: undefined }),
      aPreset(1, {
        slug: "official",
        name: "Official only",
        version: 3,
        rules: rules({ default: { classes: ["official"] }, advanced: { order: ["g1"] } }),
      }),
      aPreset(2, {
        slug: "cheap-usd",
        name: "Cheapest, in dollars",
        currency: "USD",
        keys: 12_345,
        rules: rules({ default: { sort: "price", max_price: { prompt: "3", completion: "15" } } }),
      }),
      aPreset(3, {
        scope: "site",
        slug: "verified",
        name: "Verified, fastest first",
        rules: rules({
          default: { sort: "latency", verified_only: true },
          rules: [{ author: "openai", sort: "price" }],
        }),
      }),
      aPreset(4, { scope: "site", slug: "old", name: "Retired", disabled: true, keys: 0 }),
    ],
    defaults: new Map([
      ["platform", { preset_id: uuid(16, 1), version: 2 }],
      [siteId, { preset_id: uuid(16, 3), version: 1 }],
    ]),
    limits: { rev: 1, limits: { kept_versions: 80 }, defaults },
    suspension: { suspended: false },
  };
}

// The most a layer holds by default, with the longest names and slugs, numbers at their bounds, a node that does not
// count keys, and presets suspended. The first platform preset has the longest name, the most keys, and is both the
// platform's and the site's default, so its disable dialog says everything it can.
function worstRouting(): RoutingState {
  const routing = usualRouting();
  routing.presets = Array.from({ length: 100 }, (_, n) =>
    aPreset(n + 10, {
      scope: n < 50 ? "platform" : "site",
      slug: long(`preset-${n}-`, 64),
      name: n === 0 ? long("Official sources at official quality, cheapest first ", 100) : `プリセット ${n}`,
      version: Number.MAX_SAFE_INTEGER,
      keys: n % 2 ? undefined : Number.MAX_SAFE_INTEGER,
      rules: rules({
        default: { sort: "throughput", classes: ["official", "cloud", "relay"], verified_only: true },
        rules: Array.from({ length: 100 }, (_, r) => ({ model: `vendor/${long("model-", 100)}${r}` })),
      }),
    }),
  );
  routing.limits = {
    rev: Number.MAX_SAFE_INTEGER,
    limits: {
      account: { active: 500, total: 5000 },
      kept_versions: 500,
      global: { presets: 1_000_000, bytes: 2 * 1024 * 1024 * 1024 },
    },
    defaults,
  };
  routing.defaults = new Map([
    ["platform", { preset_id: uuid(16, 10), version: Number.MAX_SAFE_INTEGER }],
    [siteId, { preset_id: uuid(16, 10), version: Number.MAX_SAFE_INTEGER }],
  ]);
  routing.suspension = { suspended: true, since: at };
  return routing;
}

const sourceClasses = ["official", "cloud", "relay"].map((slug, position) => ({
  slug,
  name: slug[0]?.toUpperCase() + slug.slice(1),
  name_zh: { official: "官方直连", cloud: "云厂商", relay: "中转站" }[slug] ?? slug,
  description: "",
  position,
  fallback: slug === "relay",
  system: true,
  rules: { match: "any" as const, conditions: [] },
  revision: 1,
  updated_at: at,
}));

const layerOf = (state: AdminState, scope: "platform" | "site") => ({
  data: routingOf(state)
    .presets.filter((preset) => preset.scope === scope)
    .sort((a, b) => Number(a.disabled) - Number(b.disabled))
    .map(withSummary),
});

function create(state: AdminState, scope: "platform" | "site", body: Schemas["LayerRoutingPresetCreate"]) {
  const live = routingOf(state).presets.filter((preset) => preset.scope === scope && !preset.disabled);
  if (live.some((preset) => preset.slug === body.slug)) {
    return fail(409, "name_taken");
  }
  const preset = aPreset(routingOf(state).presets.length + 100, {
    scope,
    slug: body.slug,
    name: body.name,
    currency: body.currency,
    rules: body.rules,
    keys: 0,
  });
  routingOf(state).presets.push(preset);
  return { status: 201, json: preset } as const;
}

function update({ body, params, state }: Ctx<"/api/admin/v1/routing-presets/{preset_id}", "put">) {
  const preset = routingOf(state).presets.find((p) => p.id === params.preset_id && !p.disabled);
  if (!preset) {
    return fail(404, "not_found");
  }
  if (preset.id === system) {
    return fail(403, "forbidden");
  }
  if (body.revision !== preset.revision) {
    return fail(409, "stale_revision");
  }
  const changed = JSON.stringify(body.rules) !== JSON.stringify(preset.rules);
  Object.assign(preset, {
    name: body.name,
    rules: body.rules,
    currency: body.currency,
    revision: preset.revision + 1,
    version: preset.version + (changed ? 1 : 0),
  });
  return ok(preset);
}

function disable({ body, params, state }: Ctx<"/api/admin/v1/routing-presets/{preset_id}", "delete">) {
  const preset = routingOf(state).presets.find((p) => p.id === params.preset_id && !p.disabled);
  if (!preset) {
    return fail(404, "not_found");
  }
  if (body.revision !== preset.revision) {
    return fail(409, "stale_revision");
  }
  if ([...routingOf(state).defaults.values()].some((d) => d.preset_id === preset.id)) {
    return fail(409, "preset_in_use");
  }
  preset.disabled = true;
  preset.revision += 1;
  return { status: 204, json: undefined } as const;
}

function setDefault(state: AdminState, layer: string, body: Schemas["LayerRoutingDefaultUpdate"]) {
  const current = routingOf(state).defaults.get(layer) ?? { version: 0 };
  if (body.version !== current.version) {
    return fail(409, "stale_revision");
  }
  const next = { preset_id: body.preset_id ?? undefined, version: current.version + 1 };
  routingOf(state).defaults.set(layer, next);
  return ok(next);
}

const ofSite = (id: string | undefined) => id === siteId;

export const routingHandlers: Handler<AdminState>[] = [
  route("get", `${base}/source-classes`, reads("sites.manage"), () => ok({ classes: sourceClasses })),
  route("get", `${base}/routing-presets`, reads("sites.manage"), ({ state }) => ok(layerOf(state, "platform"))),
  route("post", `${base}/routing-presets`, changes("sites.manage"), ({ body, state }) =>
    create(state, "platform", body),
  ),
  route("get", `${base}/sites/{site_id}/routing-presets`, reads("sites.manage"), ({ params, state }) =>
    ofSite(params.site_id) ? ok(layerOf(state, "site")) : fail(404, "not_found"),
  ),
  route("post", `${base}/sites/{site_id}/routing-presets`, changes("sites.manage"), ({ body, params, state }) =>
    ofSite(params.site_id) ? create(state, "site", body) : fail(404, "not_found"),
  ),
  route("post", `${base}/routing-presets/checks`, safe("sites.manage"), ({ body }) => ok(checkOf(body.rules))),
  route("put", `${base}/routing-presets/{preset_id}`, changes("sites.manage"), update),
  route("delete", `${base}/routing-presets/{preset_id}`, changes("sites.manage"), disable),
  route("get", `${base}/settings/routing`, reads("sites.manage"), ({ state }) =>
    ok(routingOf(state).defaults.get("platform") ?? { version: 0 }),
  ),
  route("put", `${base}/settings/routing`, changes("sites.manage"), ({ body, state }) =>
    setDefault(state, "platform", body),
  ),
  route("get", `${base}/sites/{site_id}/routing-default`, reads("sites.manage"), ({ params, state }) =>
    ofSite(params.site_id) ? ok(routingOf(state).defaults.get(siteId) ?? { version: 0 }) : fail(404, "not_found"),
  ),
  route("put", `${base}/sites/{site_id}/routing-default`, changes("sites.manage"), ({ body, params, state }) =>
    ofSite(params.site_id) ? setDefault(state, siteId, body) : fail(404, "not_found"),
  ),
  route("get", `${base}/settings/routing-limits`, reads("sites.manage"), ({ state }) => ok(routingOf(state).limits)),
  route("put", `${base}/settings/routing-limits`, changes("sites.manage"), ({ body, state }) => {
    const routing = routingOf(state);
    if (body.rev !== routing.limits.rev) {
      return fail(409, "stale_revision");
    }
    routing.limits = { ...routing.limits, rev: body.rev + 1, limits: body.limits };
    return ok(routing.limits);
  }),
  route("get", `${base}/settings/routing-suspension`, reads("sites.manage"), ({ state }) =>
    ok(routingOf(state).suspension),
  ),
  route("put", `${base}/settings/routing-suspension`, changes("sites.manage"), ({ body, state }) => {
    const routing = routingOf(state);
    if (body.suspended !== routing.suspension.suspended) {
      routing.suspension = body.suspended ? { suspended: true, since: new Date().toISOString() } : { suspended: false };
    }
    return ok(routing.suspension);
  }),
];
