import type { AdminSchemas } from "@axiom/api";

type Supplier = AdminSchemas["Supplier"];
type Credential = AdminSchemas["SupplierCredential"];
export type Mode = AdminSchemas["SupplierMode"];

export const modes = ["relay", "direct", "cloud"] as const satisfies readonly Mode[];

// How Axiom reaches a supplier; a server of an earlier release had only relays and does not say.
export const modeOf = (supplier: Pick<Supplier, "mode">): Mode => supplier.mode ?? "relay";

// What the calls of the last day say of a supplier or a line (getOperationsMonitor's lines, by the last attempt): its
// share of all the calls, and the share of its own that did not fail. Nothing is said of one that had no calls.
export type Traffic = { calls: number; share: number; rate?: number; firstTokenMs?: number };

type MonitorLine = AdminSchemas["MonitorLine"];

function add(into: Map<string, { calls: number; failures: number; ms?: number }>, key: string, line: MonitorLine) {
  const was = into.get(key) ?? { calls: 0, failures: 0 };
  into.set(key, { calls: was.calls + line.calls, failures: was.failures + line.failures, ms: line.first_token_ms_p50 });
}

export function trafficOf(lines: readonly MonitorLine[]) {
  const total = lines.reduce((sum, line) => sum + line.calls, 0);
  const suppliers = new Map<string, { calls: number; failures: number; ms?: number }>();
  const groups = new Map<string, { calls: number; failures: number; ms?: number }>();
  for (const line of lines) {
    add(suppliers, line.supplier_id, line);
    add(groups, line.group_id, line);
  }
  const traffic = (counted: { calls: number; failures: number; ms?: number } | undefined): Traffic => {
    const calls = counted?.calls ?? 0;
    return {
      calls,
      share: total > 0 ? calls / total : 0,
      rate: counted && calls > 0 ? (calls - counted.failures) / calls : undefined,
      firstTokenMs: counted?.ms,
    };
  };
  return {
    ofSupplier: (id: string) => traffic(suppliers.get(id)),
    ofGroup: (id: string) => traffic(groups.get(id)),
  };
}

// The keys pasted into a box, one per line or separated by spaces: each counted once, in the order pasted, with how
// many repeated an earlier one and are skipped.
export function pastedKeys(text: string) {
  const all = text.split(/\s+/).filter(Boolean);
  const keys = [...new Set(all)];
  return { keys, duplicates: all.length - keys.length };
}

// What a service account's JSON key says of itself that is not secret, read as it is pasted; anything else is not one.
export function serviceAccount(text: string): { project: string; email: string } | null {
  try {
    const key: unknown = JSON.parse(text);
    if (typeof key !== "object" || key === null) return null;
    const { project_id, client_email, private_key } = key as Record<string, unknown>;
    return typeof project_id === "string" && typeof client_email === "string" && typeof private_key === "string"
      ? { project: project_id, email: client_email }
      : null;
  } catch {
    return null;
  }
}

// A credential as a list names it: a pasted key by its last four characters, a cloud credential by its region, and a
// relay's key by the supplier's own reference of it.
export function credentialName(credential: Pick<Credential, "last4" | "region" | "upstream_key_ref">) {
  if (credential.last4) return `…${credential.last4}`;
  return credential.region ?? credential.upstream_key_ref;
}

// A credential that routing can pick now.
export const usableCredential = (credential: Pick<Credential, "status">) => credential.status === "active";

// Names grouped by their model's family, largest first; the models are each family's, once each.
export function byFamily(rows: readonly AdminSchemas["SupplierModel"][], none: string) {
  const families = new Map<string, { names: string[]; models: Set<string> }>();
  for (const row of rows) {
    const family = row.family ?? none;
    const into = families.get(family) ?? { names: [], models: new Set<string>() };
    into.names.push(row.upstream_name);
    if (row.model_id) into.models.add(row.model_id);
    families.set(family, into);
  }
  return [...families]
    .map(([family, { names, models }]) => ({ family, names, models: [...models] }))
    .sort((a, b) => b.names.length - a.names.length);
}
