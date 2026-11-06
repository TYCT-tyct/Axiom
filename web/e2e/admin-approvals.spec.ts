import { expect, test } from "@playwright/test";
import { uuid } from "./admin-data.ts";
import { approvalsOf } from "./admin-operations.ts";
import { close, dialog, open, send } from "./admin-page.ts";
import { apps } from "./apps.ts";
import { words, zhText } from "./words.ts";

// The operations approvals against the mocked API (admin-operations.ts): the list and its state filter, what an
// approval's page shows of the caps it was weighed against, and what a decision says it did, read from the answer.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

const credit = `/approvals/${uuid(17, 1)}`;
const approvePath = `**/api/admin/v1/operations/approvals/${uuid(17, 1)}/approve`;

test.describe("operations approvals", () => {
  test("lists every approval, narrows by state from the address, and opens one", async ({ page }) => {
    await open(page, "/approvals");
    const list = page.getByRole("table", { name: zhText("approvals.list") });
    await expect(list.getByRole("row")).toHaveCount(9);
    await page.getByLabel(zhText("approvals.filters.state")).selectOption("pending");
    await page.getByRole("button", { name: zhText("filter.apply") }).click();
    expect(new URL(page.url()).searchParams.get("state")).toBe("pending");
    await expect(list.getByRole("row")).toHaveCount(5);
    await list.getByRole("link", { name: zhText("approvals.risk.gift") }).click();
    await expect(page.getByRole("heading", { level: 1, name: zhText("approvals.risk.gift") })).toBeVisible();
  });

  test("an approved credit grant says it was given, and the page offers no second decision", async ({ page }) => {
    const state = await open(page, credit);
    const caps = page.getByRole("table", { name: zhText("approvals.cap.title") });
    await expect(page.locator("p", { hasText: zhText("approvals.cap.amount") })).toContainText("25.00");
    await expect(caps.getByRole("row").filter({ hasText: zhText("approvals.cap.daily") })).toContainText("500.00");
    await expect(page.getByText(approvalsOf(state)[0]?.agent_reason ?? "")).toBeVisible();
    await page.getByRole("button", { name: zhText("approvals.approve") }).click();
    await expect(dialog(page)).toContainText("25.00");
    await send(page, "checked the incident and the account");
    await expect(dialog(page).getByText(zhText("approvals.done.gift"))).toBeVisible();
    expect(approvalsOf(state)[0]?.state).toBe("approved");
    expect(approvalsOf(state)[0]?.decision_reason).toBe("checked the incident and the account");
    await close(page);
    await expect(page.getByRole("button", { name: zhText("approvals.approve") })).toHaveCount(0);
    await expect(page.getByText(zhText("approvals.state.approved"), { exact: true })).toBeVisible();
  });

  test("an approval whose target changed says the operation did not run", async ({ page }) => {
    const state = await open(page, credit);
    await page.route(
      approvePath,
      (route) =>
        route.fulfill({
          json: {
            ...approvalsOf(state)[0],
            state: "approved",
            operation_state: "rejected",
            error_code: "unprocessable",
          },
        }),
      { times: 1 },
    );
    await page.getByRole("button", { name: zhText("approvals.approve") }).click();
    await send(page);
    await expect(dialog(page).getByText(words("approvals.refused"))).toContainText("unprocessable");
  });

  test("an approval decided elsewhere meanwhile is refused with why", async ({ page }) => {
    const state = await open(page, credit);
    await page.getByRole("button", { name: zhText("approvals.reject") }).click();
    const approval = approvalsOf(state)[0];
    if (approval) approval.state = "expired";
    await send(page);
    await expect(dialog(page).getByText(zhText("approvals.conflict"))).toBeVisible();
  });

  test("a sale price shows its envelope, and a measurement that could not be made says so", async ({ page }) => {
    await open(page, `/approvals/${uuid(17, 3)}`);
    const table = page.getByRole("table", { name: zhText("approvals.envelope.title") });
    await expect(table.getByRole("row").filter({ hasText: zhText("approvals.envelope.change") })).toContainText(
      "12.5%",
    );
    await expect(table.getByRole("row").filter({ hasText: zhText("approvals.envelope.margin") })).toContainText(
      zhText("approvals.envelope.unknownCost"),
    );
  });
});
