import type { AdminSchemas } from "@axiom/api";
import type { Open } from "./open";

export type StepKey = "connected" | "groups" | "keys" | "unit";

export type Step = { key: StepKey; done: boolean; failed?: number; open?: Open };

// What the checklist reads from a supplier: only the fields that decide the steps, so it can be tested without a page.
export type StepInput = {
  status: AdminSchemas["SupplierStatus"];
  groups: Pick<AdminSchemas["SupplierGroup"], "id" | "present" | "selected" | "key_state">[];
  credentials: Pick<AdminSchemas["SupplierCredential"], "group_id" | "status">[];
  hasUnitValue: boolean;
};

// The steps from a new supplier to one that takes traffic, read off what the sync stored: connected, groups and prices
// read, a key for every group that needs one, and the unit of its balance valued. Groups in use are chosen by the sync
// unless a person chose otherwise, so the checklist no longer asks for that choice; it still shows a bound key for a
// node of an earlier release, which has no key_state.
export function steps({ status, groups, credentials, hasUnitValue }: StepInput): Step[] {
  const present = groups.filter((group) => group.present);
  const bound = new Set(
    credentials
      .filter((credential) => credential.status !== "erased" && credential.status !== "binding")
      .map((credential) => credential.group_id),
  );
  const keyStates = present.map(
    (group) => group.key_state ?? (bound.has(group.id) ? "ready" : group.selected ? "pending" : "unavailable"),
  );
  return [
    { key: "connected", done: status !== "connecting" && status !== "needs_attention" },
    { key: "groups", done: present.length > 0 },
    {
      key: "keys",
      done: keyStates.every((state) => state === "ready" || state === "unavailable"),
      failed: keyStates.filter((state) => state === "failed").length,
    },
    { key: "unit", done: hasUnitValue, open: { kind: "unit" } },
  ];
}
