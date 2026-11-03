import type { AdminSchemas } from "@axiom/api";
import { createInstance } from "i18next";
import { renderToStaticMarkup } from "react-dom/server";
import { I18nextProvider } from "react-i18next";
import { beforeAll, expect, it } from "vitest";
import en from "../locales/en";
import { RefundActions } from "./refunds";

const i18n = createInstance();

beforeAll(async () => {
  await i18n.init({ lng: "en", defaultNS: "admin", resources: { en: { admin: en } } });
});

const frozen: AdminSchemas["PaymentRefund"] = { id: "rfd_1", state: "frozen" };

function render(refund: AdminSchemas["PaymentRefund"], viewer: { self?: boolean; firstBySelf?: boolean } = {}) {
  return renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <RefundActions
        refund={refund}
        self={viewer.self ?? false}
        firstBySelf={viewer.firstBySelf ?? false}
        onDecide={() => {}}
      />
    </I18nextProvider>,
  );
}

it("offers the approval to an operator who can release the refund", () => {
  const html = render(frozen);
  expect(html).toContain(">Approve<");
  expect(html).toContain(">Reject<");
});

it("tells the requester that another administrator must approve", () => {
  const html = render(frozen, { self: true });
  expect(html).not.toContain(">Approve<");
  expect(html).toContain("Another administrator must approve this refund.");
});

it("tells the first approver that someone else must release it", () => {
  const html = render(frozen, { firstBySelf: true });
  expect(html).not.toContain(">Approve<");
  expect(html).toContain("You recorded the first approval.");
});

it("names the second approval on a refund above the site's limit", () => {
  expect(render({ ...frozen, second_required: true, first_approved_by: "usr_first" })).toContain(">Second approval<");
});

it("offers nothing once the refund is released", () => {
  expect(render({ ...frozen, approved_by: "usr_second" })).toBe("");
});
