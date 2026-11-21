import type { AdminState, Schemas } from "./admin-data.ts";
import { base, type Ctx, changes, reads, route } from "./admin-route.ts";
import { fail, type Handler, ok } from "./mock.ts";

// Site brand colour, workspace limits and login limits kept beside the admin mock handlers so admin-api.ts and
// admin-data.ts stay within the file-length limit.

export type SiteExtras = {
  brand: Schemas["SiteBrand"];
  workspaceLimit: Schemas["WorkspaceLimitView"];
  loginLimits: Schemas["LoginLimitsView"];
};

const loginDefaults: Schemas["LoginLimits"] = {
  ip_attempt_burst: 20,
  ip_attempts_per_minute: 60,
  email_sign_in_failures: 10,
  user_code_failures: 10,
  challenge_code_attempts: 5,
};

const extras = new WeakMap<AdminState, SiteExtras>();

export function siteExtrasOf(state: AdminState): SiteExtras {
  let current = extras.get(state);
  if (!current) {
    current = {
      brand: { brand_color: "#2563eb" },
      workspaceLimit: { max_workspaces: 25, effective: 25 },
      loginLimits: { limits: { ip_attempt_burst: 30 }, defaults: { ...loginDefaults } },
    };
    extras.set(state, current);
  }
  return current;
}

// Seeds the workspace limit and credential RPM fields on the mocked accounts and suppliers without growing admin-data.ts.
export function startSiteExtras(state: AdminState) {
  const inherited = siteExtrasOf(state).workspaceLimit.effective;
  for (const account of state.accounts.values()) {
    account.workspace_limit ??= { effective: inherited };
  }
  for (const detail of state.suppliers) {
    const first = detail.credentials[0];
    if (first && first.requests_per_minute === undefined) {
      first.requests_per_minute = 600;
    }
  }
}

function getSiteBrand({ params, state }: Ctx<"/api/admin/v1/sites/{site_id}/brand", "get">) {
  return params.site_id === state.siteId ? ok(siteExtrasOf(state).brand) : fail(404, "not_found");
}

function putSiteBrand({ body, params, state }: Ctx<"/api/admin/v1/sites/{site_id}/brand", "put">) {
  if (params.site_id !== state.siteId) {
    return fail(404, "not_found");
  }
  if (body.brand_color !== "" && !/^#[0-9a-fA-F]{6}$/.test(body.brand_color)) {
    return fail(400, "invalid_request", { "/brand_color": "must be #rrggbb or empty" });
  }
  const next: Schemas["SiteBrand"] = body.brand_color === "" ? {} : { brand_color: body.brand_color.toLowerCase() };
  siteExtrasOf(state).brand = next;
  return ok(next);
}

const validWorkspaceLimit = (value: number | undefined) =>
  value === undefined || (Number.isInteger(value) && value >= 1 && value <= 1_000);

function getSiteWorkspaceLimit({ params, state }: Ctx<"/api/admin/v1/sites/{site_id}/workspace-limit", "get">) {
  return params.site_id === state.siteId ? ok(siteExtrasOf(state).workspaceLimit) : fail(404, "not_found");
}

function putSiteWorkspaceLimit({ body, params, state }: Ctx<"/api/admin/v1/sites/{site_id}/workspace-limit", "put">) {
  if (params.site_id !== state.siteId) {
    return fail(404, "not_found");
  }
  if (!validWorkspaceLimit(body.max_workspaces)) {
    return fail(400, "invalid_request", { "/max_workspaces": "must be between 1 and 1000" });
  }
  const next: Schemas["WorkspaceLimitView"] =
    body.max_workspaces === undefined
      ? { effective: 50 }
      : { max_workspaces: body.max_workspaces, effective: body.max_workspaces };
  siteExtrasOf(state).workspaceLimit = next;
  return ok(next);
}

function putAccountWorkspaceLimit({
  body,
  params,
  state,
}: Ctx<"/api/admin/v1/accounts/{account_id}/workspace-limit", "put">) {
  const account = state.accounts.get(params.account_id ?? "");
  if (!account) {
    return fail(404, "not_found");
  }
  if (!validWorkspaceLimit(body.max_workspaces)) {
    return fail(400, "invalid_request", { "/max_workspaces": "must be between 1 and 1000" });
  }
  const inherited = siteExtrasOf(state).workspaceLimit.effective;
  account.workspace_limit =
    body.max_workspaces === undefined
      ? { effective: inherited }
      : { max_workspaces: body.max_workspaces, effective: body.max_workspaces };
  return ok(account.workspace_limit);
}

const loginBounds = [
  ["ip_attempt_burst", 10_000],
  ["ip_attempts_per_minute", 10_000],
  ["email_sign_in_failures", 1_000],
  ["user_code_failures", 1_000],
  ["challenge_code_attempts", 100],
] as const;

function validLoginLimits(limits: Schemas["LoginLimits"]) {
  return loginBounds.every(([name, max]) => {
    const value = limits[name];
    return value === undefined || (Number.isInteger(value) && value >= 1 && value <= max);
  });
}

function getSiteLoginLimits({ params, state }: Ctx<"/api/admin/v1/sites/{site_id}/login-limits", "get">) {
  return params.site_id === state.siteId ? ok(siteExtrasOf(state).loginLimits) : fail(404, "not_found");
}

function putSiteLoginLimits({ body, params, state }: Ctx<"/api/admin/v1/sites/{site_id}/login-limits", "put">) {
  if (params.site_id !== state.siteId) {
    return fail(404, "not_found");
  }
  if (!validLoginLimits(body.limits)) {
    return fail(400, "invalid_request");
  }
  const next: Schemas["LoginLimitsView"] = { limits: body.limits, defaults: { ...loginDefaults } };
  siteExtrasOf(state).loginLimits = next;
  return ok(next);
}

export const siteExtraHandlers: Handler<AdminState>[] = [
  route("get", `${base}/sites/{site_id}/brand`, reads("sites.manage"), getSiteBrand),
  route("put", `${base}/sites/{site_id}/brand`, changes("sites.manage"), putSiteBrand),
  route("get", `${base}/sites/{site_id}/workspace-limit`, reads("sites.manage"), getSiteWorkspaceLimit),
  route("put", `${base}/sites/{site_id}/workspace-limit`, changes("sites.manage"), putSiteWorkspaceLimit),
  route("get", `${base}/sites/{site_id}/login-limits`, reads("sites.manage"), getSiteLoginLimits),
  route("put", `${base}/sites/{site_id}/login-limits`, changes("sites.manage"), putSiteLoginLimits),
  route("put", `${base}/accounts/{account_id}/workspace-limit`, changes("sites.manage"), putAccountWorkspaceLimit),
];
