import { expect, test } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { opsControlOf } from "./admin-ops-control-api.ts";
import { adminUrl } from "./apps.ts";
import { signedInAs } from "./mock-auth.ts";
import { words, zhText } from "./words.ts";

test.use({ baseURL: adminUrl() });

test.describe("operations credentials", () => {
  test("creates and rotates credentials showing one-time secrets that Escape cannot dismiss, and revokes", async ({
    page,
  }) => {
    const state = await mockAdmin(page);
    signedInAs(state, "mfa");

    await page.goto("/operations-credentials");
    await expect(page.getByRole("heading", { level: 1, name: zhText("operations.credentials.title") })).toBeVisible();
    await expect(page.getByText("hermes-prod")).toBeVisible();

    // Create a credential and verify the one-time token dialog cannot be dismissed with Escape.
    await page.getByRole("button", { name: zhText("operations.credentials.new") }).click();
    const create = page.getByRole("dialog");
    await create.locator("input[name=name]").fill("triage-agent");
    await create.locator('input[name="perm:operations.write"]').check();
    await create.locator("input[name=proposer_vendors]").fill("deepseek, openai");
    await create.getByLabel(words("change.reason")).fill("issue agent credential for triage");
    await create.locator("button[type=submit]").click();

    const secret = page.getByRole("dialog", { name: zhText("operations.secret.createdTitle") });
    await expect(secret).toBeVisible();
    await expect(secret.getByText(/op-ax-v1-mock-/)).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(secret).toBeVisible();
    await secret.getByRole("button", { name: zhText("operations.secret.saved") }).click();
    await expect(secret).toHaveCount(0);
    await expect(page.getByText("triage-agent")).toBeVisible();

    // Rotate the newly created credential and verify Escape still cannot dismiss the rotated secret dialog.
    await page.getByRole("button", { name: "轮换凭证 triage-agent" }).click();
    const rotate = page.getByRole("dialog");
    await rotate.locator("input[name=overlap_hours]").fill("12");
    await rotate.getByLabel(words("change.reason")).fill("scheduled monthly rotation");
    await rotate.locator("button[type=submit]").click();

    const rotated = page.getByRole("dialog", { name: zhText("operations.secret.rotatedTitle") });
    await expect(rotated).toBeVisible();
    await expect(rotated.getByText(/op-ax-v1-rot-/)).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(rotated).toBeVisible();
    await rotated.getByRole("button", { name: zhText("operations.secret.saved") }).click();
    await expect(rotated).toHaveCount(0);

    // Revoke requires typing the credential's name.
    await page.getByRole("button", { name: "撤销凭证 triage-agent" }).click();
    const revoke = page.getByRole("dialog");
    await revoke.getByLabel(words("change.reason")).fill("decommission triage agent");
    await revoke.locator("input[name=confirm]").fill("triage-agent");
    await revoke.locator("button[type=submit]").click();
    await expect(revoke.getByText(/已撤销凭证/)).toBeVisible();
    await revoke
      .getByRole("button", { name: zhText("change.close") })
      .last()
      .click();
    await expect(page.getByRole("button", { name: "撤销凭证 triage-agent" })).toHaveCount(0);
  });

  test("denies access without operations.manage", async ({ page }) => {
    const state = await mockAdmin(page);
    state.permissions = ["users.view"];
    signedInAs(state, "mfa");

    await page.goto("/operations-credentials");
    await expect(page.getByRole("heading", { name: "没有权限" })).toBeVisible();
    await page.goto("/operations-limits");
    await expect(page.getByRole("heading", { name: "没有权限" })).toBeVisible();
  });
});

test.describe("operations limits and emergency stop", () => {
  test("repairs paused cap counters, edits caps with stale reload, edits agent limits and price envelope, and runs emergency stop", async ({
    page,
  }) => {
    const state = await mockAdmin(page);
    signedInAs(state, "mfa");

    await page.goto("/operations-limits");
    await expect(page.getByRole("heading", { level: 1, name: zhText("operations.limits.title") })).toBeVisible();
    await expect(page.getByText(zhText("operations.limits.cap.paused"))).toBeVisible();

    // Repair paused refund cap counters.
    await page.getByRole("button", { name: /修复计数 .* 订单退款/ }).click();
    const repair = page.getByRole("dialog");
    await repair.getByLabel(words("change.reason")).fill("reconcile counters after ledger check");
    await repair.locator("button[type=submit]").click();
    await expect(repair.getByText(/已重新核对/)).toBeVisible();
    await repair
      .getByRole("button", { name: zhText("change.close") })
      .last()
      .click();
    await expect(page.getByText(zhText("operations.limits.cap.paused"))).toHaveCount(0);

    // Edit platform top-up cap policy with stale revision reload.
    await page.getByRole("button", { name: "修改资金上限 平台 供应商充值" }).click();
    const capEdit = page.getByRole("dialog");
    const topUp = opsControlOf(state).caps.find((c) => c.category === "money.top_up");
    if (!topUp) {
      throw new Error("expected money.top_up cap policy");
    }
    topUp.rev += 1;
    topUp.limits.single_max = "60000000000";

    await capEdit.locator("input[name=single_max]").fill("75");
    await capEdit.getByLabel(words("change.reason")).fill("raise autonomous single top-up");
    await capEdit.locator("button[type=submit]").click();
    await expect(capEdit.getByRole("alert")).toContainText(zhText("change.stale"));

    await capEdit.getByRole("button", { name: zhText("change.reload") }).click();
    await expect(capEdit.locator("input[name=single_max]")).toHaveValue("60");
    await capEdit.locator("input[name=single_max]").fill("75");
    await capEdit.getByLabel(words("change.reason")).fill("raise single top-up after reload");
    await capEdit.locator("button[type=submit]").click();
    await expect(capEdit.getByText(/已保存 平台/)).toBeVisible();
    await capEdit
      .getByRole("button", { name: zhText("change.close") })
      .last()
      .click();
    await expect(page.getByText(/单笔 US\$75\.00/)).toBeVisible();

    // Edit agent execution limits.
    await page.getByRole("button", { name: zhText("operations.limits.agent.edit") }).click();
    const limits = page.getByRole("dialog");
    await limits.locator("input[name=requests_per_minute]").fill("240");
    await limits.locator("input[name=alerts_per_hour]").fill("25");
    await limits.getByLabel(words("change.reason")).fill("tighten rate ceiling");
    await limits.locator("button[type=submit]").click();
    await expect(limits.getByText(/已保存运营执行上限/)).toBeVisible();
    await limits
      .getByRole("button", { name: zhText("change.close") })
      .last()
      .click();
    await expect(page.getByText("240")).toBeVisible();

    // Edit price envelope.
    await page.getByRole("button", { name: /修改站点包络/ }).click();
    const env = page.getByRole("dialog");
    await env.locator("input[name=min_margin_bps]").fill("1850");
    await env.getByLabel(words("change.reason")).fill("raise floor margin");
    await env.locator("button[type=submit]").click();
    await expect(env.getByText(/已保存站点/)).toBeVisible();
    await env
      .getByRole("button", { name: zhText("change.close") })
      .last()
      .click();
    await expect(page.getByText("18.5%")).toBeVisible();

    // Emergency stop requires typed confirmation "STOP".
    await page.getByRole("button", { name: zhText("operations.limits.stop.open") }).click();
    const stop = page.getByRole("dialog");
    await stop.getByLabel(words("change.reason")).fill("suspected runaway agent");
    await stop.locator("button[type=submit]").click();
    await expect(stop.getByRole("alert").getByText(zhText("change.confirmMismatch"))).toBeVisible();

    await stop.locator("input[name=confirm]").fill("STOP");
    await stop.locator("button[type=submit]").click();
    await expect(stop.getByText(/已撤销 2 个有效凭证，并取消 4 条待审批提案/)).toBeVisible();
    await stop
      .getByRole("button", { name: zhText("change.close") })
      .last()
      .click();
    expect(opsControlOf(state).credentials.every((c) => c.revoked_at !== undefined)).toBe(true);
  });
});
