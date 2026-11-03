import { expect, it } from "vitest";
import { approvalOutcome } from "./refund-approval";

it("reads a first approval from the answer", () => {
  expect(approvalOutcome({ first_approved_by: "usr_first" })).toBe("first");
});

it("reports a release the dialog did not expect", () => {
  expect(approvalOutcome({ approved_by: "usr_second", second_required: true })).toBe("released");
});

it("keeps an offline release apart from a channel one", () => {
  expect(approvalOutcome({ approved_by: "usr_second", via: "offline" })).toBe("offline");
});
