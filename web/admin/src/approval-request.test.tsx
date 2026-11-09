// @vitest-environment jsdom
import type { AdminSchemas } from "@axiom/api";
import { expect, it } from "vitest";
import { RequestFacts } from "./approval-request";
import { renderInDom } from "./test-dom";

type Approval = AdminSchemas["OperationsApproval"];

// A refund paid in USD on a site that keeps its caps in CNY: the owner reads the credits it frees, the cash it returns
// in the currency it was paid in, and what the caps count, each in its own currency, not only the converted amount.
it("shows a refund's credits, cash and cap amount each in its currency", async () => {
  const approval: Approval = {
    id: "a",
    state: "pending",
    tier: "human",
    requested_at: "2026-11-06T08:00:00Z",
    expires_at: "2026-11-06T08:30:00Z",
    operation_id: "o",
    credential_id: "c",
    tool: "refund_request",
    target: { kind: "order", id: "ord_1" },
    risk: "refund",
    params: {},
    summary: {
      credits: "5000000000",
      site_currency: "USD",
      cash: "1000",
      pay_currency: "USD",
      amount: "70000000000",
      currency: "CNY",
    },
    agent_reason: "r",
    operation_state: "pending_approval",
    decidable: true,
  };
  const { container, unmount } = await renderInDom(<RequestFacts approval={approval} />);
  const text = container.textContent ?? "";
  for (const want of [
    "Credits to refund",
    "$5.00",
    "Cash returned, as paid",
    "$10.00",
    "Counted against the refund caps",
    "70.00",
  ]) {
    expect(text).toContain(want);
  }
  expect(text).not.toContain("Amount");
  await unmount();
});
