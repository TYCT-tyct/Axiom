import type { AdminSchemas } from "@axiom/api";

// Every state a supplier can be in, for choosing one to filter by.
export const supplierStatuses = [
  "connecting",
  "active",
  "low_balance",
  "needs_attention",
  "paused",
  "removing",
  "removed",
] as const satisfies readonly AdminSchemas["SupplierStatus"][];

export function supplierStatus(value: unknown) {
  return supplierStatuses.find((status) => status === value);
}
