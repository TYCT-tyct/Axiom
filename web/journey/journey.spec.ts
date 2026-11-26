import { createHmac } from "node:crypto";
import { expect, type Page, test } from "@playwright/test";
import { readNode } from "./node.ts";

// One user's first day on a real node (cmd/axiom/journey_test.go), the golden path every pull request that touches it
// walks: register with an invite from the website, which holds the console's sign-in pages in its own document
// (site/src/home/sign-in.tsx), sign in on the console's own page, turn on two-step verification and sign in again with a
// code, make a key, top up through the payment gateway the node is given, call a model in each of the three formats,
// whole and streamed, through the supplier it is given, find the calls and their charges in the console, and switch the
// key off. The console has no page for topping up yet (#317), so that step is the console's API, called with the page's
// own session as the page would.

const password = "a journey horse battery";
const grant = 10_000_000_000n; // billionths: ten dollars
const credits = { amount: grant.toString(), currency: "USD" };
const model = "claude-opus-5-5";
const hello = [{ role: "user", content: "Hello" }];
const messagesCall = { model, max_tokens: 256, messages: hello };
// Each format reaches the Messages endpoint the node's supplier serves the model on (journey_test.go), whole and streamed.
const calls = [
  { path: "/v1/chat/completions", body: { model, max_tokens: 256, messages: hello } },
  { path: "/v1/responses", body: { model, max_output_tokens: 256, input: "Hello" } },
  { path: "/v1/messages", body: messagesCall },
];

test("register, sign in with two steps, key, top-up, calls, their charges, and a key switched off", async ({
  page,
}) => {
  const node = readNode();
  const consoleApi = `${node.console}/api/console/v1`;
  const email = "journey@example.com";

  await page.goto(`${node.console}/`);
  // the sign-in pages stand ready in the website's frame once its first screen is drawn
  await expect(page.locator(".tr .lg")).toBeAttached();
  await page.evaluate(() => Object.assign(window, { sameDocument: true }));
  await page.getByRole("banner").getByRole("link", { name: "注册", exact: true }).click();
  await expect(page).toHaveURL(/\/console\/register$/);
  await page.getByLabel("邀请码").fill(node.invite_code);
  expect(await page.evaluate(() => (window as unknown as { sameDocument?: boolean }).sameDocument)).toBe(true);
  await page.getByLabel("邮箱").fill(email);
  await page.getByLabel("密码").fill(password);
  await page.getByRole("button", { name: "注册", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();

  // Registering signs in; signing out, in the account menu, and in again is the sign-in itself.
  await signOut(page);
  await signIn(page, email);
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();

  // Two-step verification, turned on with a code of the secret the setup hands out, as an authenticator app would.
  // Each step's code is spent once, so the sign-in after it uses a later step than the one that turned it on.
  await page.goto(`${node.console}/console/security`);
  const setup = page.waitForResponse((r) => r.url().endsWith("/auth/totp/setup"));
  await page.getByRole("button", { name: "设置验证器 App" }).click();
  const { secret } = await (await setup).json();
  const enable = page.getByRole("dialog", { name: "设置验证器 App" });
  await enable.getByLabel("密码").fill(password);
  await enable.getByLabel("验证器里的 6 位验证码").fill(totp(secret, -1));
  await enable.getByRole("button", { name: "开启", exact: true }).click();
  const codes = page.getByRole("dialog", { name: "保存你的恢复码" });
  await expect(codes.getByRole("listitem")).toHaveCount(10);
  await codes.getByRole("button", { name: "我已保存" }).click();
  await expect(page.getByText(/^已设置 ·/)).toBeVisible();

  await signOut(page);
  await signIn(page, email);
  await expect(page.getByRole("heading", { level: 1, name: "两步验证" })).toBeVisible();
  await page.getByLabel("验证码或恢复码").fill(totp(secret, 1));
  const verified = page.waitForResponse((r) => r.url().endsWith("/auth/mfa") && r.request().method() === "POST");
  await page.getByRole("button", { name: "验证", exact: true }).click();
  const { csrf_token: csrf } = await (await verified).json();
  // The code is the sign-in: the page it was left from opens again, with two-step verification on.
  await expect(page.getByRole("heading", { level: 1, name: "两步验证" })).toHaveCount(0);
  await page.goto(`${node.console}/console/security`);
  await expect(page.getByText(/^已设置 ·/)).toBeVisible();

  await page.goto(`${node.console}/console/keys`);
  await page.getByRole("button", { name: "创建 key", exact: true }).click();
  await page.getByLabel("名称", { exact: true }).fill("Journey");
  await page.getByRole("button", { name: "创建", exact: true }).click();
  const shown = page.getByRole("dialog", { name: "复制你的 key" });
  const key = (await shown.locator("code").first().textContent()) ?? "";
  await shown.getByRole("button", { name: "我已保存" }).click();
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
  const paid = await page.request.post(`${consoleApi}/orders/${id}/verify`, { headers: changes, data: {} });
  expect(paid.status(), await paid.text()).toBe(200);
  expect((await paid.json()).state).toBe("paid");

  await page.goto(`${node.console}/console/balance`);
  await expect(page.getByText("US$10.00", { exact: true }).first()).toBeVisible();

  const auth = { Authorization: `Bearer ${key}`, "anthropic-version": "2023-06-01" };
  for (const { path, body } of calls) {
    for (const stream of [false, true]) {
      const call = await page.request.post(`${node.api}${path}`, { headers: auth, data: { ...body, stream } });
      const text = await call.text();
      expect(call.status(), text).toBe(200);
      expect(text, `${path} stream ${stream}`).toContain("Hello");
    }
  }
  expect((await (await page.request.get(node.calls)).text()).split("\n")).toEqual(Array(6).fill("/v1/messages"));

  // The calls are recorded and charged after the response, in batches, so they are read again until all are there.
  // What the account has left is what it was given less what the calls were charged, and nothing stays reserved.
  await expect(async () => {
    const listed = await (await page.request.get(`${consoleApi}/calls?limit=50`)).json();
    expect(listed.data).toHaveLength(calls.length * 2);
    const charged = listed.data.reduce(
      (sum: bigint, c: { price: { amount: string } }) => sum + BigInt(c.price.amount),
      0n,
    );
    expect(charged).toBeGreaterThan(0n);
    const balance = await (await page.request.get(`${consoleApi}/balance`)).json();
    expect([balance.balance.amount, balance.reserved.amount]).toEqual([(grant - charged).toString(), "0"]);
  }).toPass({ timeout: 30_000 });
  await page.goto(`${node.console}/console/calls`);
  const rows = page.getByRole("region", { name: "调用记录" }).locator('[data-a="lrow"]').filter({ hasText: model });
  await expect(rows.filter({ hasText: "成功" }).filter({ hasText: "25 / 15" })).toHaveCount(calls.length * 2);
  await page.goto(`${node.console}/console/statement`);
  await expect(page.getByRole("row", { name: /充值/ })).toContainText("US$10.00");
  await expect(page.getByRole("row", { name: /调用扣费/ }).first()).toContainText("-US$0.00045");

  // A key switched off is refused from its next call on.
  await page.goto(`${node.console}/console/keys`);
  await page.getByRole("row", { name: /^Journey / }).click();
  const edit = page.getByRole("dialog", { name: "修改 key" });
  await edit.getByLabel("停用这个 key").check();
  await edit.getByRole("button", { name: "保存" }).click();
  await expect(edit).toHaveCount(0);
  const refused = await page.request.post(`${node.api}/v1/messages`, { headers: auth, data: messagesCall });
  expect(refused.status(), await refused.text()).toBe(401);
});

async function signOut(page: Page) {
  await page.getByRole("button", { name: "账号设置" }).click();
  await page.getByRole("button", { name: "退出登录" }).click();
}

async function signIn(page: Page, email: string) {
  await page.getByLabel("邮箱").fill(email);
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await page.getByLabel("密码", { exact: true }).fill(password);
  await page.getByRole("button", { name: "登录", exact: true }).click();
}

// totp is the code of an RFC 6238 secret, in base32, at the 30-second step `offset` steps from now: HMAC-SHA1 and six
// digits, as internal/accounts/totp.go checks it, which accepts the steps either side of the current one.
function totp(secret: string, offset: number): string {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  const bits = [...secret].map((c) => alphabet.indexOf(c).toString(2).padStart(5, "0")).join("");
  const key = Buffer.from((bits.match(/.{8}/g) ?? []).map((byte) => Number.parseInt(byte, 2)));
  const step = Buffer.alloc(8);
  step.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30_000) + offset));
  const mac = createHmac("sha1", key).update(step).digest();
  const at = (mac.at(-1) ?? 0) & 0x0f;
  return String((mac.readUInt32BE(at) & 0x7fffffff) % 1_000_000).padStart(6, "0");
}
