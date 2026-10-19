import type { components, paths } from "../packages/api/src/admin.gen.ts";
import type { AdminState } from "./admin-data.ts";
import { type Ctx as CoreCtx, createRoute, type Needs } from "./mock.ts";

// What every file of the admin's answers shares: the typed route builder of the admin contract and what an operation
// asks of a session (api/admin.yaml): the admin's operations all need a session that passed two-step verification and
// a permission, and a change besides takes an Idempotency-Key and a reason.

export const base = "/api/admin/v1";

type Login = components["schemas"]["SupplierLogin"];

// Which members a login has, in the server's order: a key compares those, never the secrets.
const loginFields = ["email", "password", "access_token", "refresh_token", "access_token_expires_at"] as const;
const members = (login: Login) => loginFields.filter((field) => login[field]);

// An origin as suppliers.NormalizeOrigin writes it, or as given when it is none.
function origin(raw: string) {
  try {
    const url = new URL(raw.toLowerCase());
    return `https://${url.hostname}${url.port ? `:${url.port}` : ""}`;
  } catch {
    return raw;
  }
}

// What the server compares a change by under one key where that is not the whole body (cmd/axiom/admin_suppliers.go).
export const identities: Record<string, Needs["identity"]> = {
  [`post ${base}/suppliers`]: (body) => {
    const b = body as components["schemas"]["SupplierCreate"];
    return [b.name, b.kind, origin(b.origin), b.upstream_account, b.settlement_currency, b.reason, members(b.login)];
  },
  [`put ${base}/suppliers/{supplier_id}/login`]: (body) => {
    const b = body as components["schemas"]["SupplierLoginUpdate"];
    return [b.reason, members(b.login)];
  },
};

const typed = createRoute<paths, AdminState>();
export const route: typeof typed = (method, path, needs, handle) =>
  typed(method, path, { ...needs, identity: identities[`${method} ${path}`] }, handle);

export type Ctx<K extends keyof paths, M extends "get" | "post" | "put" | "delete"> = CoreCtx<paths, K, M, AdminState>;

export const reads = (permission: string): Needs => ({ level: "mfa", permission });
export const changes = (permission: string): Needs => ({ level: "mfa", permission, change: true });
// A change that is safe to send again and needs no key or reason (a probe, a preview).
export const safe = (permission: string): Needs => ({ level: "mfa", permission });
