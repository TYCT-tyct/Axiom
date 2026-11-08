import { expect, test } from "@playwright/test";
import { readNode } from "./node.ts";

// One user's first day on a real node (cmd/axiom/journey_test.go): register with an invite, sign in, make a key, top up
// through the payment gateway the node is given, call a model through the supplier it is given, and find the call and
// its charge in the console. The console has no page for topping up yet (#317), so that step is the console's API, called
// with the page's own session as the page would.

const password = "a journey horse battery";
const credits = { amount: "10000000000", currency: "USD" }; // billionths: ten dollars
const messagesCall = { model: "claude-opus-5-5", max_tokens: 256, messages: [{ role: "user", content: "Hello" }] };

test("register, sign in, key, top-up, call, and see the call and its charge", async ({ page }) => {
  const node = readNode();
  const consoleApi = `${node.console}/api/console/v1`;
  const email = "journey@example.com";

  await page.goto(`${node.console}/console/register?invite=${node.invite_code}`);
  await page.getByLabel("邮箱").fill(email);
  await page.getByLabel("密码").fill(password);
  await page.getByRole("button", { name: "注册", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();

  // Registering signs in; signing out and in again is the sign-in itself.
  await page.getByRole("button", { name: "退出登录" }).click();
  await page.getByLabel("邮箱").fill(email);
  await page.getByLabel("密码").fill(password);
  const signedIn = page.waitForResponse((r) => r.url().endsWith("/auth/sign-in") && r.request().method() === "POST");
  await page.getByRole("button", { name: "登录", exact: true }).click();
  const { csrf_token: csrf } = await (await signedIn).json();
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();

  await page.goto(`${node.console}/console/keys`);
  await page.getByRole("button", { name: "创建 key", exact: true }).click();
  await page.getByLabel("名称", { exact: true }).fill("Journey");
  await page.getByRole("button", { name: "创建", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "复制你的 key" });
  const key = (await dialog.locator("code").first().textContent()) ?? "";
  await dialog.getByRole("button", { name: "我已保存" }).click();
  await expect(page.getByRole("row", { name: /Journey/ })).toBeVisible();

  // Top-ups need an https console, so the order is placed under the site's https domain, as a proxy that ends TLS
  // would pass it on: the same session, under the name its cookie has there (a __Host- cookie on https).
  const session = (await page.context().cookies()).find((c) => c.name === "axiom_session");
  const changes = {
    Host: node.pay_host,
    Origin: `https://${node.pay_host}`,
    Cookie: `__Host-axiom_session=${session?.value}`,
    "X-Csrf-Token": csrf,
  };
  const order = await page.request.post(`${consoleApi}/orders`, {
    headers: { ...changes, "Idempotency-Key": "journey-top-up" },
    data: { method: "alipay", credits },
  });
  expect(order.status(), await order.text()).toBe(201);
  const { id } = await order.json();
  await expect(await page.request.post(node.settle)).toBeOK();
  const verified = await page.request.post(`${consoleApi}/orders/${id}/verify`, { headers: changes, data: {} });
  expect(verified.status(), await verified.text()).toBe(200);
  expect((await verified.json()).state).toBe("paid");

  await page.goto(`${node.console}/console/balance`);
  await expect(page.getByText("US$10.00", { exact: true }).first()).toBeVisible();

  const call = await page.request.post(`${node.api}/v1/messages`, {
    headers: { Authorization: `Bearer ${key}`, "anthropic-version": "2023-06-01" },
    data: messagesCall,
  });
  expect(call.status(), await call.text()).toBe(200);
  expect(await (await page.request.get(node.calls)).text()).toBe("/v1/messages");

  // The call is recorded and charged after the response, in batches, so the pages are read again until they have it.
  await expect(async () => {
    await page.goto(`${node.console}/console/calls`);
    const row = page.getByRole("row", { name: /claude-opus-5-5/ });
    await expect(row).toContainText("成功", { timeout: 1000 });
    await expect(row).toContainText("25 / 15", { timeout: 1000 });
  }).toPass({ timeout: 30_000 });
  await page.goto(`${node.console}/console/statement`);
  await expect(page.getByRole("row", { name: /充值/ })).toContainText("US$10.00");
  await expect(page.getByRole("row", { name: /调用扣费/ })).toContainText("-US$0.00045");
});
