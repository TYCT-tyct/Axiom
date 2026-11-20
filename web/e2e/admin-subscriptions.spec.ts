import { expect, test } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { acct } from "./admin-scenes.ts";
import { paidGrantId, pendingRefundOtherId, pendingRefundSelfId, subscriptionsOf } from "./admin-subscriptions-api.ts";
import { adminUrl } from "./apps.ts";
import { signedInAs } from "./mock-auth.ts";
import { words, zhText } from "./words.ts";

test.use({ baseURL: adminUrl() });

test.describe("subscription plans", () => {
  test("lists active plans, toggles inactive, creates a plan and edits against revision", async ({ page }) => {
    const state = await mockAdmin(page);
    signedInAs(state, "mfa");

    await page.goto("/subscription-plans");
    await expect(page.getByRole("heading", { level: 1, name: zhText("subscriptions.title") })).toBeVisible();
    await expect(page.getByText("Pro Monthly")).toBeVisible();
    await expect(page.getByText("Legacy Starter")).toHaveCount(0);

    await page.getByText(zhText("subscriptions.includeInactive")).click();
    await expect(page.getByText("Legacy Starter")).toBeVisible();

    // Create a new plan.
    await page.getByRole("button", { name: zhText("subscriptions.new") }).click();
    const dialog = page.getByRole("dialog");
    await dialog.locator("input[name=name]").fill("Enterprise Quarterly");
    await dialog.locator("input[name=price]").fill("249.50");
    await dialog.locator("input[name=quota]").fill("50");
    await dialog.locator("input[name=window_seconds]").fill("86400");
    await dialog.locator("input[name=term_seconds]").fill("7776000");
    await dialog.getByLabel(words("change.reason")).fill("launch quarterly tier");
    await dialog.locator("button[type=submit]").click();
    await expect(dialog.getByText(/Enterprise Quarterly/)).toBeVisible();
    await dialog
      .getByRole("button", { name: zhText("change.close") })
      .last()
      .click();
    await expect(page.getByText("Enterprise Quarterly")).toBeVisible();

    // Edit Pro Monthly and verify stale revision triggers reload before saving.
    await page.getByRole("button", { name: "修改套餐 Pro Monthly" }).click();
    const edit = page.getByRole("dialog");
    const pro = subscriptionsOf(state).plans.find((plan) => plan.name === "Pro Monthly");
    if (!pro) {
      throw new Error("expected Pro Monthly in mock store");
    }
    pro.rev += 1;
    pro.name = "Pro Monthly Updated";

    await edit.locator("input[name=price]").fill("35");
    await edit.getByLabel(words("change.reason")).fill("adjust monthly price");
    await edit.locator("button[type=submit]").click();
    await expect(edit.getByRole("alert")).toContainText(zhText("change.stale"));

    await edit.getByRole("button", { name: zhText("change.reload") }).click();
    await expect(edit.locator("input[name=name]")).toHaveValue("Pro Monthly Updated");
    await edit.locator("input[name=price]").fill("35");
    await edit.getByLabel(words("change.reason")).fill("adjust monthly price after reload");
    await edit.locator("button[type=submit]").click();
    await expect(edit.getByText(/Pro Monthly Updated/)).toBeVisible();
    await edit
      .getByRole("button", { name: zhText("change.close") })
      .last()
      .click();
    await expect(page.getByText("$35.00")).toBeVisible();
  });

  test("shows empty state and hides write controls without pricing.manage", async ({ page }) => {
    const state = await mockAdmin(page, "empty");
    state.permissions = ["pricing.view"];
    signedInAs(state, "mfa");

    await page.goto("/subscription-plans");
    await expect(page.getByRole("heading", { name: zhText("subscriptions.empty.title") })).toBeVisible();
    await expect(page.getByRole("button", { name: zhText("subscriptions.new") })).toHaveCount(0);
  });
});

test.describe("account subscriptions", () => {
  test("lists subscriptions, grants an offline renewal, requests and approves a refund", async ({ page }) => {
    const state = await mockAdmin(page);
    signedInAs(state, "mfa");

    await page.goto(`/accounts/${acct(1)}`);
    await expect(page.getByRole("heading", { name: zhText("subscriptions.account.title") })).toBeVisible();
    await expect(page.getByText("Pro Monthly")).toBeVisible();
    await expect(page.getByText("Legacy Starter")).toBeVisible();

    // Grant renewal via offline payment.
    await page.getByRole("button", { name: zhText("subscriptions.account.grant") }).click();
    const grant = page.getByRole("dialog");
    await grant.locator("select[name=source]").selectOption("offline_payment");
    await grant.locator("input[name=amount]").fill("29");
    await grant.locator("input[name=channel]").fill("Wire #4421");
    await grant.getByLabel(words("change.reason")).fill("renew via wire transfer");
    await grant.locator("button[type=submit]").click();
    await expect(grant.getByText(/Pro Monthly/)).toBeVisible();
    await grant
      .getByRole("button", { name: zhText("change.close") })
      .last()
      .click();

    // Request refund (202 proposal).
    await page.getByRole("button", { name: zhText("subscriptions.account.refundRequest") }).click();
    const request = page.getByRole("dialog");
    await request.locator("input[name=grant_id]").fill(paidGrantId);
    await request.locator("input[name=credits]").fill("12.5");
    await request.getByLabel(words("change.reason")).fill("customer downgraded mid-term");
    await request.locator("button[type=submit]").click();
    await expect(request.getByRole("button", { name: zhText("subscriptions.refund.copyProposalId") })).toBeVisible();
    await request
      .getByRole("button", { name: zhText("change.close") })
      .last()
      .click();

    // Approving own proposal is refused (409 second-person rule).
    await page.getByRole("button", { name: zhText("subscriptions.account.refundApprove") }).click();
    const approve = page.getByRole("dialog");
    await approve.locator("input[name=refund_id]").fill(pendingRefundSelfId);
    await approve.getByLabel(words("change.reason")).fill("self approval check");
    await approve.locator("button[type=submit]").click();
    await expect(approve.getByRole("alert")).toContainText(zhText("subscriptions.refund.conflict"));

    // Approving another operator's pending proposal succeeds (200).
    await approve.locator("input[name=refund_id]").fill(pendingRefundOtherId);
    await approve.getByLabel(words("change.reason")).fill("verified bank refund");
    await approve.locator("button[type=submit]").click();
    await expect(approve.getByText(/已批准订阅退款/)).toBeVisible();
    await approve
      .getByRole("button", { name: zhText("change.close") })
      .last()
      .click();
  });

  test("hides grant and refund actions without credits.grant", async ({ page }) => {
    const state = await mockAdmin(page);
    state.permissions = ["users.view"];
    signedInAs(state, "mfa");

    await page.goto(`/accounts/${acct(1)}`);
    await expect(page.getByText("Pro Monthly")).toBeVisible();
    await expect(page.getByRole("button", { name: zhText("subscriptions.account.grant") })).toHaveCount(0);
    await expect(page.getByRole("button", { name: zhText("subscriptions.account.refundRequest") })).toHaveCount(0);
    await expect(page.getByRole("button", { name: zhText("subscriptions.account.refundApprove") })).toHaveCount(0);
  });
});
