// @vitest-environment jsdom
import type { AdminSchemas } from "@axiom/api";
import { expect, it } from "vitest";
import { ApprovalState } from "./approval-parts";
import { renderInDom } from "./test-dom";

type Approval = AdminSchemas["OperationsApproval"];

const approved = (operation_state: Approval["operation_state"]): Approval => ({
  id: "a",
  state: "approved",
  tier: "human",
  requested_at: "2026-11-06T08:00:00Z",
  expires_at: "2026-11-06T08:30:00Z",
  operation_id: "o",
  credential_id: "c",
  tool: "refund_request",
  target: { kind: "order", id: "ord_1" },
  risk: "refund",
  params: {},
  summary: {},
  agent_reason: "r",
  operation_state,
  decidable: false,
});

// An approved refund the channel is sending, or that it reported failed, ran: the list says so, and keeps "not run" for
// an approval whose operation the target refused.
it.each([
  ["executing", "Running", "not run"],
  ["failed", "Failed", "not run"],
  ["rejected", "Approved, not run", "Running"],
] as const)("an approved %s operation reads as %s", async (state, want, not) => {
  const { container, unmount } = await renderInDom(<ApprovalState approval={approved(state)} now={0} />);
  expect(container.textContent).toContain(want);
  expect(container.textContent).not.toContain(not);
  await unmount();
});
