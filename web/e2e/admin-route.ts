import type { paths } from "../packages/api/src/admin.gen.ts";
import type { AdminState } from "./admin-data.ts";
import { type Ctx as CoreCtx, createRoute, type Needs } from "./mock.ts";

// What every file of the admin's answers shares: the typed route builder of the admin contract and what an operation
// asks of a session (api/admin.yaml): the admin's operations all need a session that passed two-step verification and
// a permission, and a change besides takes an Idempotency-Key and a reason.

export const route = createRoute<paths, AdminState>();
export const base = "/api/admin/v1";

export type Ctx<K extends keyof paths, M extends "get" | "post" | "put" | "delete"> = CoreCtx<paths, K, M, AdminState>;

export const reads = (permission: string): Needs => ({ level: "mfa", permission });
export const changes = (permission: string): Needs => ({ level: "mfa", permission, change: true });
// A change that is safe to send again and needs no key or reason (a probe, a preview).
export const safe = (permission: string): Needs => ({ level: "mfa", permission });
