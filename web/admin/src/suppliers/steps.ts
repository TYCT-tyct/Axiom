import type { AdminSchemas } from "@axiom/api";
import type { Mode } from "./upstream";

type StepKey =
  | "connected"
  | "login"
  | "groups"
  | "keys"
  | "unit"
  | "pasted"
  | "credential"
  | "regions"
  | "line"
  | "models"
  | "cost";

export type Step = { key: StepKey; done: boolean; failed?: number };

// What the checklist reads from a supplier: only the fields that decide the steps, so it can be tested without a page.
export type StepInput = {
  mode?: Mode;
  status: AdminSchemas["SupplierStatus"];
  groups: Pick<AdminSchemas["SupplierGroup"], "id" | "present" | "selected" | "key_state">[];
  credentials: Pick<AdminSchemas["SupplierCredential"], "group_id" | "status" | "region">[];
  endpoints?: Pick<AdminSchemas["SupplierEndpoint"], "cost_state">[];
  hasUnitValue: boolean;
  // A cloud platform with no regions to choose (Azure OpenAI): its one credential is its one region.
  regionless?: boolean;
};

const read = (status: StepInput["status"]) => status !== "connecting";

// A relay's keys: one for every group that needs one, read off what the sync stored. The sync chooses the groups in use
// unless a person chose otherwise; a node of an earlier release has no key_state and is read by its bound keys.
function relayKeys({ groups, credentials }: StepInput): Step {
  const present = groups.filter((group) => group.present);
  const bound = new Set(
    credentials
      .filter((credential) => credential.status !== "erased" && credential.status !== "binding")
      .map((credential) => credential.group_id),
  );
  const keyStates = present.map(
    (group) => group.key_state ?? (bound.has(group.id) ? "ready" : group.selected ? "pending" : "unavailable"),
  );
  return {
    key: "keys",
    done: present.length > 0 && keyStates.every((state) => state === "ready" || state === "unavailable"),
    failed: keyStates.filter((state) => state === "failed").length,
  };
}

// The steps from a new supplier to one that takes traffic, in the words of the way it was connected (ADMIN_HOWTO,
// 第六轮「上号方式」): a relay is connected, logged in, has its groups and their keys, and its unit valued; a direct API
// and a cloud platform have their keys or credential checked, their one line, the models read and a cost to go by.
// The board's last step, the quality check, has no answer in the admin API yet, so it is not listed.
export function steps(input: StepInput): Step[] {
  const { status, groups, credentials, endpoints = [] } = input;
  const live = credentials.filter((credential) => credential.status === "active");
  const shared = [
    { key: "line", done: groups.some((group) => group.selected) },
    { key: "models", done: endpoints.length > 0 },
    { key: "cost", done: endpoints.some((endpoint) => endpoint.cost_state === "known") },
  ] as const;
  switch (input.mode ?? "relay") {
    case "direct":
      return [
        { key: "connected", done: read(status) },
        { key: "pasted", done: live.length > 0, failed: credentials.filter((c) => c.status === "rejected").length },
        ...shared,
      ];
    case "cloud":
      return [
        { key: "credential", done: live.length > 0 },
        {
          key: "regions",
          done: live.some((credential) => credential.region) || (input.regionless === true && live.length > 0),
        },
        ...shared,
      ];
    default:
      return [
        { key: "connected", done: read(status) },
        { key: "login", done: read(status) && status !== "needs_attention" },
        { key: "groups", done: groups.some((group) => group.present) },
        relayKeys(input),
        { key: "unit", done: input.hasUnitValue },
      ];
  }
}
