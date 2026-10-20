import type { AdminSchemas } from "@axiom/api";

// What a supplier page can open a dialog about.
export type Open =
  | { kind: "pause" | "resume" | "remove" | "login" | "unit" }
  | { kind: "group"; group: AdminSchemas["SupplierGroup"] }
  | { kind: "bind"; key: AdminSchemas["UpstreamKey"] };
