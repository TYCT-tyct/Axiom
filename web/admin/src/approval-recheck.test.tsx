// @vitest-environment jsdom
import type { AdminSchemas } from "@axiom/api";
import { useQuery } from "@tanstack/react-query";
import { act } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { RawData } from "./approval-request";
import { decidable } from "./approvals";
import en from "./locales/en";
import { approvalQuery, recheck } from "./queries";
import { renderInDom } from "./test-dom";

type Approval = AdminSchemas["OperationsApproval"];

const approval: Approval = {
  id: "a",
  state: "pending",
  tier: "human",
  requested_at: "2026-11-06T08:00:00Z",
  expires_at: "2026-11-06T08:30:00Z",
  operation_id: "o",
  credential_id: "c",
  tool: "supplier_top_up_propose",
  target: { kind: "supplier", id: "s" },
  risk: "top_up",
  params: {},
  summary: {},
  agent_reason: "r",
  operation_state: "pending_approval",
  decidable: true,
};

// What the server answers each read, in turn: the test moves it past the expiry.
const answers = vi.hoisted(() => ({ next: {} as unknown }));
const get = vi.hoisted(() =>
  vi.fn(() => Promise.resolve({ data: JSON.stringify(answers.next), response: new Response() })),
);

vi.mock("@axiom/api", async (importOriginal) =>
  (await import("./test-dom")).withAdminApi(importOriginal, { GET: get }),
);

afterEach(() => vi.useRealTimers());

// settle lets a read the page sent come back and reach the page: react-query hands its results on through a timer.
async function settle() {
  for (let i = 0; i < 3; i++) {
    await act(async () => vi.advanceTimersByTimeAsync(1));
  }
}

function Probe() {
  const query = useQuery(approvalQuery("a"));
  return query.data ? <p>{decidable(query.data) ? "open" : "lapsed"}</p> : null;
}

// A page left open past an approval's expiry asks the server again and follows its answer, without a clock of its own;
// once the server says it cannot be decided, the page stops asking.
it("follows the server's answer while the page stays open across the expiry", async () => {
  vi.useFakeTimers();
  answers.next = approval;
  const { container, unmount } = await renderInDom(<Probe />);
  await settle();
  expect(container.textContent).toBe("open");
  answers.next = { ...approval, decidable: false };
  await act(async () => vi.advanceTimersByTimeAsync(recheck));
  await settle();
  expect(container.textContent).toBe("lapsed");
  const reads = get.mock.calls.length;
  await act(async () => vi.advanceTimersByTimeAsync(recheck * 4));
  expect(get.mock.calls.length).toBe(reads);
  await unmount();
});

// A reason the page cannot name points at the raw data, which shows it as the server sent it.
it("shows the server's reasons in the raw data", async () => {
  const { container, unmount } = await renderInDom(
    <RawData approval={{ ...approval, reasons: [{ code: "later_code" as "always" }] }} />,
  );
  expect(container.textContent).toContain(en.approvals.raw.reasons);
  expect(container.textContent).toContain("later_code");
  await unmount();
});
