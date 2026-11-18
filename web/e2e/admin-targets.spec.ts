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
  test("the dashboard compares each target and the editor replaces the month", async ({ page }) => {
    const state = await open(page, "");
    await expect(page.getByText(zhText("overview.targets.description"))).toBeVisible();
    await expect(page.getByText(words("overview.targets.target")).first()).toBeVisible();
    for (const label of ["paidUsers", "monthlyRevenue", "grossMargin", "retention"] as const) {
      await expect(page.getByText(words(`overview.${label}`)).first()).toBeVisible();
    }

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
    await expect(page.getByText(words("overview.targets.ahead")).first()).toBeVisible();
  });

  test("business figures drill into the records behind them with the same window", async ({ page }) => {
    await open(page, "");
    const link = (key: string) => page.getByRole("link", { name: new RegExp(zhText(`overview.${key}`)) });
    const address = async (key: string) => new URL((await link(key).getAttribute("href")) ?? "", "http://axiom");
    const [paid, revenue, margin, retention] = await Promise.all([
      address("paidUsers"),
      address("monthlyRevenue"),
      address("grossMargin"),
      address("retention"),
    ]);
    for (const url of [paid, revenue]) {
      expect(url.pathname).toBe("/orders");
      expect(url.searchParams.get("paid")).toBe("batches");
      expect(url.searchParams.get("since")).toBeTruthy();
      expect(url.searchParams.get("until")).toBeTruthy();
    }
    expect(margin.pathname).toBe("/calls");
    expect(margin.searchParams.get("site_id")).toBeTruthy();
    expect(margin.searchParams.get("since")).toBeTruthy();
    expect(margin.searchParams.get("until")).toBeTruthy();
    expect(retention.pathname).toBe("/orders");
    expect(retention.searchParams.get("paid")).toBe("batches");
    expect(retention.searchParams.get("since")).toMatch(/-01T00:00:00\.000Z$/);
    expect(retention.searchParams.get("until")).toBe(paid.searchParams.get("until"));

    await link("monthlyRevenue").click();
    await page.waitForURL(/\/orders\?/);
    const landed = new URL(page.url());
    expect(landed.searchParams.get("paid")).toBe("batches");
    expect(landed.searchParams.get("since")).toBe(revenue.searchParams.get("since"));
    expect(landed.searchParams.get("until")).toBe(revenue.searchParams.get("until"));
    // Exact: the URL changes before the overview unmounts, and its notes contain the title as a substring.
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
    await open(page, "", "empty");
    await expect(page.getByText(zhText("overview.targets.empty"))).toBeVisible();
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

  test("at 360 the rank tables keep their headers and account ids on one line", async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 900 });
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
    // The 31-character fixture can fit the column exactly on a font that renders slightly narrower; a longer id makes
    // this a test of the clip, not of the font.
    const state = await mockAdmin(page, "worst");
    const full = `acct_${"0".repeat(40)}1`;
    state.calls = state.calls.map((call) => ({ ...call, account_id: full as typeof call.account_id }));
    signedInAs(state, "mfa");
    await page.goto("/");
    for (const table of await page.getByRole("table").all()) {
      for (const head of await table.getByRole("columnheader").all()) {
        expect((await head.boundingBox())?.height ?? 0).toBeLessThan(40);
      }
    }

    const accounts = page.getByRole("table", { name: zhText("overview.topAccounts") });
    const first = accounts.getByRole("row").nth(1).getByRole("cell").first();
    await expect(first).toContainText("acct_");
    await expect(first).toContainText(full.slice(-8));
    const text = first.locator("[title]").first();
    await expect(text).toHaveAttribute("title", full);
    expect((await first.boundingBox())?.height ?? 0).toBeLessThan(50);
    const head = text.locator("span[aria-hidden]").first();
    const box = await head.evaluate((el) => {
      const cell = el.closest("[role=cell]")?.getBoundingClientRect();
      const rect = el.getBoundingClientRect();
      return {
        right: rect.right,
        cell: cell?.right ?? 0,
        scroll: el.scrollWidth,
        client: el.clientWidth,
      };
    });
    expect(box.right).toBeLessThanOrEqual(box.cell + 0.5);
    expect(box.scroll).toBeGreaterThan(box.client);
    await first.getByRole("button", { name: zhText("copy.copy") }).click();
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(full);
  });

  test("a call's time is shown in the viewer's zone, and the zone is named", async ({ page }) => {
    await open(page, "/calls");
    await expect(page.getByRole("row").nth(1)).toBeVisible();
    await expect(page.getByText(/GMT\+8/).first()).toBeVisible();
    await expect(page.getByText(/\bUTC\b/)).toHaveCount(0);
  });
});
