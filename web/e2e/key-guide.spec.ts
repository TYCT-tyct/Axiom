import { expect, type Page, test } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";

const labels = {
  zh: {
    create: "创建 key",
    name: "名称",
    submit: "创建",
    title: "复制你的 key",
    error: "接入地址加载失败",
    retry: "重试",
  },
  en: {
    create: "Create key",
    name: "Name",
    submit: "Create",
    title: "Copy your key",
    error: "Couldn’t load the connection address",
    retry: "Try again",
  },
};

async function createSecret(page: Page, language: keyof typeof labels = "zh") {
  const text = labels[language];
  await page.addInitScript((value) => localStorage.setItem("axiom.language", value), language);
  await page.goto("/console/keys");
  await page.getByRole("button", { name: text.create, exact: true }).click();
  await page.getByLabel(text.name, { exact: true }).fill("Guide test");
  await page.getByRole("button", { name: text.submit, exact: true }).click();
  return page.getByRole("dialog", { name: text.title });
}

for (const language of ["zh", "en"] as const) {
  test(`a failed site lookup can be retried without losing the key (${language})`, async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    let fail = true;
    await page.route("**/api/console/v1/site", (route) =>
      fail
        ? route.fulfill({ status: 403, json: { error: { code: "forbidden", message: "test failure" } } })
        : route.fallback(),
    );
    const dialog = await createSecret(page, language);
    const key = (await dialog.locator("code").textContent()) ?? "";
    await expect(dialog.getByRole("alert")).toHaveText(labels[language].error);
    await expect(dialog.locator("pre")).toHaveCount(0);
    fail = false;
    await dialog.getByRole("button", { name: labels[language].retry }).click();
    await expect(dialog.locator("pre")).toHaveCount(6);
    await expect(dialog.getByRole("alert")).toHaveCount(0);
    await expect(dialog.locator("code")).toHaveText(key);
    await expect(dialog.locator("pre").nth(4)).toContainText('[model_providers.axiom]\nname = "Axiom"');
    expect(state.log.filter((line) => line === "POST /keys")).toHaveLength(1);
  });
}

test("a site without an API domain keeps the key visible without a guide or an error", async ({ page }) => {
  const state = await mockConsole(page);
  delete state.site.api_base_url;
  signedInAs(state);
  const dialog = await createSecret(page);
  await expect(dialog.locator("code")).toBeVisible();
  await expect(dialog.locator("pre")).toHaveCount(0);
  await expect(dialog.getByRole("alert")).toHaveCount(0);
});

for (const name of ["复制", "复制 Claude Code"]) {
  test(`clipboard denial leaves ${name} short and announces the failure once`, async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    const dialog = await createSecret(page);
    await expect(dialog.locator("pre")).toHaveCount(6);
    await page.evaluate(() => {
      navigator.clipboard.writeText = async () => {
        throw new Error("clipboard denied for test");
      };
    });
    const copy = dialog.getByRole("button", { name, exact: true });
    await copy.click();
    await expect(dialog.getByRole("status").filter({ hasText: "没能复制，请手动选中" })).toHaveCount(1);
    await expect(copy).toHaveText(name);
    await expect(dialog.getByText("没能复制，请手动选中", { exact: true })).toHaveCount(1);
    await page.evaluate(() => {
      navigator.clipboard.writeText = async () => {};
    });
    await copy.press("Enter");
    await expect(dialog.getByRole("button", { name: "已复制", exact: true })).toBeVisible();
    await expect(dialog.getByText("没能复制，请手动选中", { exact: true })).toHaveCount(0);
  });
}
