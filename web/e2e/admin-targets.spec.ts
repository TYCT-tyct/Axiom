import { expect, type Page, test } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { close, dialog, open, send } from "./admin-page.ts";
import { apps } from "./apps.ts";
import { signedInAs } from "./mock-auth.ts";
import { words, zhText } from "./words.ts";

// The target editor and the dashboard's target lines against the mocked API (admin-api.ts, admin-fixtures.ts).

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

const press = (page: Page, key: string) => page.getByRole("button", { name: words(key) }).click();

test.describe("business targets", () => {
  test("the month's goals show each target against the month so far and the editor replaces the month", async ({
    page,
  }) => {
    const state = await open(page, "");
    const goals = page.getByRole("region", { name: words("goals:title") });
    await expect(goals).toBeVisible();
    for (const goal of ["paid", "margin", "retention"] as const) {
      await expect(goals.getByText(words(`goals:${goal}`)).first()).toBeVisible();
    }
    await expect(goals.getByRole("progressbar")).not.toHaveCount(0);

    await press(page, "overview.targets.edit");
    const editor = dialog(page);
    await expect(editor.getByLabel(words("overview.targets.calls"))).toHaveValue("45");
    await expect(editor.getByLabel(words("overview.targets.paidUsers"))).toHaveValue("5");
    await expect(editor.getByLabel(words("overview.targets.monthlyRevenue"))).toHaveValue("5000");
    await expect(editor.getByLabel(words("overview.targets.grossMargin"))).toHaveValue("45");
    await expect(editor.getByLabel(words("overview.targets.retention"))).toHaveValue("70");
    await editor.getByLabel(words("overview.targets.calls")).fill("50");
    await editor.getByLabel(words("overview.targets.errors")).fill("5");
    await editor.getByLabel(words("overview.targets.paidUsers")).fill("6");
    await editor.getByLabel(words("overview.targets.grossMargin")).fill("40.5");
    await editor.getByLabel(words("overview.targets.retention")).fill("75");
    await send(page);
    await expect(editor.getByText(zhText("overview.targets.saved"))).toBeVisible();

    const month = new Date().toISOString().slice(0, 7);
    expect(state.targets.get(month)?.calls).toBe(50);
    expect(state.targets.get(month)?.errors).toBe(5);
    expect(state.targets.get(month)?.paid_users).toBe(6);
    expect(state.targets.get(month)?.gross_margin).toBe(4050);
    expect(state.targets.get(month)?.retention).toBe(7500);

    await close(page);
    // The goals read the new targets: the margin bar's figure is now against 40.5%, retention against 75%.
    await expect(goals).toContainText("40.5%");
    await expect(goals).toContainText("75%");
  });

  test("the goals for paying accounts and retention open the records behind them with the month's window", async ({
    page,
  }) => {
    await open(page, "");
    const goals = page.getByRole("region", { name: words("goals:title") });
    const address = async (key: string) =>
      new URL(
        (await goals.getByRole("link", { name: zhText(`goals:${key}`) }).getAttribute("href")) ?? "",
        "http://axiom",
      );
    const [paid, retention] = await Promise.all([address("paid"), address("retention")]);
    expect(paid.pathname).toBe("/orders");
    expect(paid.searchParams.get("paid")).toBe("batches");
    expect(paid.searchParams.get("since")).toMatch(/-01T00:00:00\.000Z$/);
    expect(paid.searchParams.get("until")).toBeTruthy();
    expect(retention.pathname).toBe("/orders");
    expect(retention.searchParams.get("paid")).toBe("batches");
    // The cohort is the month before, so the list starts there and ends where the window does.
    expect(retention.searchParams.get("since")).toMatch(/-01T00:00:00\.000Z$/);
    expect(retention.searchParams.get("since")).not.toBe(paid.searchParams.get("since"));
    expect(retention.searchParams.get("until")).toBe(paid.searchParams.get("until"));

    await goals.getByRole("link", { name: zhText("goals:paid") }).click();
    await page.waitForURL(/\/orders\?/);
    const landed = new URL(page.url());
    expect(landed.searchParams.get("paid")).toBe("batches");
    expect(landed.searchParams.get("since")).toBe(paid.searchParams.get("since"));
    // Exact: the URL changes before the dashboard unmounts, and its words contain the title as a substring.
    await expect(page.getByText(zhText("orders.paidBatches.title"), { exact: true })).toBeVisible();
    await expect(page.getByText("bank transfer")).toBeVisible();
  });

  test("an old node's four-field read writes only those four and keeps the commercial targets", async ({ page }) => {
    const state = await mockAdmin(page, "usual");
    state.oldTargetRead = true;
    signedInAs(state, "mfa");
    await page.goto("");
    const month = new Date().toISOString().slice(0, 7);
    const before = state.targets.get(month);
    await press(page, "overview.targets.edit");
    const editor = dialog(page);
    await expect(editor.getByLabel(words("overview.targets.paidUsers"))).toHaveCount(0);
    await editor.getByLabel(words("overview.targets.calls")).fill("51");
    await send(page);
    await expect(editor.getByText(zhText("overview.targets.saved"))).toBeVisible();
    expect(state.targets.get(month)?.calls).toBe(51);
    expect(state.targets.get(month)?.paid_users).toBe(before?.paid_users);
  });

  test("a full clear is refused by a node without the full-replacement route", async ({ page }) => {
    const state = await open(page, "");
    state.oldTargetWrite = true;
    const month = new Date().toISOString().slice(0, 7);
    const before = state.targets.get(month);
    await press(page, "overview.targets.edit");
    const editor = dialog(page);
    for (const field of [
      "calls",
      "errors",
      "revenue",
      "cost",
      "paidUsers",
      "monthlyRevenue",
      "grossMargin",
      "retention",
    ] as const) {
      await editor.getByLabel(words(`overview.targets.${field}`)).fill("");
    }
    await send(page);

    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("errors.not_found"));
    await expect(dialog(page).getByText(zhText("overview.targets.saved"))).toHaveCount(0);
    expect(state.targets.get(month)).toEqual(before);
  });

  test("an editor with an old revision reloads before it can save", async ({ page }) => {
    const state = await open(page, "");
    const month = new Date().toISOString().slice(0, 7);
    const current = state.targets.get(month);
    if (!current) {
      throw new Error("the usual target fixture is missing");
    }
    await press(page, "overview.targets.edit");
    const editor = dialog(page);

    // Another administrator changes the month after this form was filled. The form's revision and values are now old;
    // saving it must be refused instead of restoring the calls target it read.
    state.targets.set(month, { ...current, calls: 99, rev: current.rev + 1 });
    await editor.getByLabel(words("overview.targets.errors")).fill("5");
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("change.stale"));

    await press(page, "change.reload");
    await expect(editor.getByLabel(words("overview.targets.calls"))).toHaveValue("99");
    await editor.getByLabel(words("overview.targets.calls")).fill("50");
    await send(page);
    await expect(dialog(page).getByText(zhText("overview.targets.saved"))).toBeVisible();
    expect(state.targets.get(month)?.calls).toBe(50);
    expect(state.targets.get(month)?.errors).toBe(10);
  });

  test("a month with no target says so and still offers the editor", async ({ page }) => {
    await open(page, "", "usual", { targets: new Map() });
    await expect(page.getByText(zhText("goals:none"))).toBeVisible();
    await expect(page.getByRole("button", { name: words("overview.targets.edit") })).toBeVisible();
  });

  test("the dashboard and the editor fit a 360px screen", async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 900 });
    await open(page, "", "worst");
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);

    await press(page, "overview.targets.edit");
    const box = await dialog(page).boundingBox();
    expect(box?.x ?? -1).toBeGreaterThanOrEqual(0);
    expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(360);
  });

  test("the month control reads in the page's language and says it is UTC", async ({ page }) => {
    await open(page, "");
    const now = new Date();
    const month = now.toISOString().slice(5, 7);
    const year = now.toISOString().slice(0, 4);
    const group = page.getByRole("group", { name: zhText("overview.targets.month") });
    await expect(group.getByLabel(zhText("month.month"))).toHaveValue(month);
    await expect(group.getByLabel(zhText("month.year"))).toHaveValue(year);
    const monthName = new Intl.DateTimeFormat("zh-CN", { month: "short", timeZone: "UTC" }).format(
      new Date(Date.UTC(2000, Number(month) - 1, 1)),
    );
    await expect(group.getByLabel(zhText("month.month")).locator("option:checked")).toHaveText(monthName);
    await expect(page.getByText(/UTC.*GMT\+8/)).toBeVisible();
    await expect(page.locator('input[type="month"]')).toHaveCount(0);

    const yearSelect = group.getByLabel(zhText("month.year"));
    const next = String(Number(year) + 1);
    await yearSelect.selectOption(next);
    await expect(yearSelect).toHaveValue(next);
    await expect(group.getByLabel(zhText("month.month"))).toHaveValue(month);

    const high = String(Number(year) + 10);
    await yearSelect.selectOption(high);
    await expect(yearSelect).toHaveValue(high);
    await yearSelect.selectOption(String(Number(high) + 1));
    await expect(yearSelect).toHaveValue(String(Number(high) + 1));

    const low = String(Number(year) - 10);
    await yearSelect.selectOption(low);
    await expect(yearSelect).toHaveValue(low);
    await yearSelect.selectOption(String(Number(low) - 1));
    await expect(yearSelect).toHaveValue(String(Number(low) - 1));

    await yearSelect.selectOption(next);
    await expect(yearSelect).toHaveValue(next);
    await press(page, "overview.targets.edit");
    const label = new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "long", timeZone: "UTC" }).format(
      new Date(Date.UTC(Number(next), Number(month) - 1, 1)),
    );
    await expect(
      dialog(page).getByRole("heading", { name: zhText("overview.targets.title").replace("{{month}}", label) }),
    ).toBeVisible();
  });

  test("a call's time is shown in the viewer's zone, and the zone is named", async ({ page }) => {
    await open(page, "/calls");
    await expect(page.getByRole("row").nth(1)).toBeVisible();
    await expect(page.getByText(/GMT\+8/).first()).toBeVisible();
    await expect(page.getByText(/\bUTC\b/)).toHaveCount(0);
  });
});
