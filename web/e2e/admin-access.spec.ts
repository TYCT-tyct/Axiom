import { expect, type Page, test } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { uuid } from "./admin-data.ts";
import { acct } from "./admin-scenes.ts";
import { apps } from "./apps.ts";
import { knownEmail, knownPassword, limitedEmail, mfaEmail, signedInAs } from "./mock-auth.ts";
import { words, zhText } from "./words.ts";

// Who may use the admin and what they see: the sign-in with two-step verification the admin requires, a person who has
// not set it up yet, the permissions that decide the pages, and the four states of a view of data.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

function collectErrors(page: Page) {
  const errors: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error") {
      errors.push(message.text());
    }
  });
  page.on("pageerror", (error) => errors.push(error.message));
  return errors;
}

async function signIn(page: Page, email: string, password = knownPassword) {
  await page.getByLabel("邮箱").fill(email);
  await page.getByLabel("密码").fill(password);
  await page.getByRole("button", { name: "登录", exact: true }).click();
}

const h1 = (page: Page, name: string) => page.getByRole("heading", { level: 1, name });

test.describe("signing in", () => {
  test("a password and then a code open the first page the session may use", async ({ page }) => {
    const errors = collectErrors(page);
    const state = await mockAdmin(page);
    await page.goto("/");
    await expect(h1(page, "登录")).toBeVisible();
    await expect(page).toHaveTitle("登录 · Axiom");

    await signIn(page, mfaEmail, "wrong");
    await expect(page.getByRole("alert")).toHaveText(/邮箱或密码不对/);
    await signIn(page, mfaEmail);
    await expect(h1(page, "两步验证")).toBeVisible();
    await page.getByLabel("验证码或恢复码").fill("000000");
    await page.getByRole("button", { name: "验证" }).click();
    await expect(page.getByRole("alert")).toHaveText(/验证码不对/);
    await page.getByLabel("验证码或恢复码").fill("123456");
    await page.getByRole("button", { name: "验证" }).click();

    await expect(h1(page, zhText("nav.overview"))).toBeVisible();
    expect(new URL(page.url()).pathname).toBe("/");
    expect(state.log.filter((line) => line === "POST /auth/sign-in")).toHaveLength(2);
    // the browser logs the 401s (no session yet, a wrong password, a wrong code) itself
    expect(errors.filter((line) => !line.startsWith("Failed to load resource"))).toEqual([]);
  });

  test("a two-step sign-in that is spent is signed out, and stays so after a reload", async ({ page }) => {
    const state = await mockAdmin(page);
    await page.goto("/");
    await signIn(page, mfaEmail);
    await expect(h1(page, "两步验证")).toBeVisible();
    for (let i = 0; i < 5; i++) {
      await page.getByLabel("验证码或恢复码").fill("000000");
      await page.getByRole("button", { name: "验证" }).click();
      await expect(page.getByRole("alert")).toHaveText(/验证码不对/);
    }
    // a right code once the attempts are spent: the server answers that the sign-in is over
    await page.getByLabel("验证码或恢复码").fill("123456");
    await page.getByRole("button", { name: "验证" }).click();
    await expect(h1(page, "登录")).toBeVisible();
    await expect(page.getByText("上一次登录已经失效，请重新登录。")).toBeVisible();
    expect(state.level).toBe("none");
    await page.reload();
    await expect(h1(page, "登录")).toBeVisible();
  });

  test("a rate limit says how long to wait", async ({ page }) => {
    await mockAdmin(page);
    await page.goto("/sign-in");
    await signIn(page, limitedEmail);
    await expect(page.getByRole("alert")).toHaveText(/30 秒/);
  });

  test("someone who has not set up two-step verification can reach nothing until they do", async ({ page }) => {
    const state = await mockAdmin(page);
    await page.goto("/models");
    await signIn(page, knownEmail);
    await expect(h1(page, "先开启两步验证")).toBeVisible();
    await page.goto("/users");
    await expect(h1(page, "先开启两步验证")).toBeVisible();
    expect(state.log.filter((line) => line.startsWith("GET /users"))).toEqual([]);

    await page.getByRole("button", { name: "开启两步验证" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("密码").fill(knownPassword);
    await dialog.getByLabel(/验证码/).fill("123456");
    await dialog.locator("button[type=submit]").click();

    // The new session moves the page on, and the recovery codes stay until they are kept.
    await expect(page.getByRole("dialog", { name: "保存你的恢复码" })).toBeVisible();
    await expect(page.getByRole("listitem").first()).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog", { name: "保存你的恢复码" })).toBeVisible();
    await page.getByRole("button", { name: "我已保存" }).click();
    // The gate returns to the page the visitor asked for before enrolling, which was /users, not the dashboard.
    await expect(h1(page, zhText("nav.users"))).toBeVisible();
    expect(new URL(page.url()).pathname).toBe("/users");
  });

  test("a session that ends while a page is open sends the user to sign in and back to that page", async ({ page }) => {
    const state = await mockAdmin(page);
    signedInAs(state, "mfa");
    await page.goto("/users");
    await expect(page.getByText("user1@example.com")).toBeVisible();
    state.level = "none";
    await page.getByRole("link", { name: zhText("nav.suppliers") }).click();
    await expect(h1(page, "登录")).toBeVisible();
    state.mfaEnabled = true;
    await signIn(page, mfaEmail);
    await page.getByLabel("验证码或恢复码").fill("123456");
    await page.getByRole("button", { name: "验证" }).click();
    await expect(h1(page, zhText("nav.suppliers"))).toBeVisible();
    expect(new URL(page.url()).pathname).toBe("/suppliers");
  });
});

test.describe("permissions", () => {
  test("the sidebar offers only the pages the session holds a permission for", async ({ page }) => {
    const state = await mockAdmin(page);
    state.permissions = ["catalog.view", "pricing.view"];
    signedInAs(state, "mfa");
    await page.goto("/");
    await expect(h1(page, zhText("nav.models"))).toBeVisible();
    const nav = page.getByRole("navigation", { name: "主导航" });
    await expect(nav.getByRole("link")).toHaveText([zhText("nav.models"), zhText("nav.pricing")]);
    // viewing is not managing: no creating buttons
    await expect(page.getByRole("button", { name: words("models.draft.open") })).toHaveCount(0);
    await expect(page.getByRole("button", { name: words("models.authors.add") })).toHaveCount(0);
  });

  test("a session with no permission is told so, and a page it may not see says so", async ({ page }) => {
    const state = await mockAdmin(page);
    state.permissions = [];
    signedInAs(state, "mfa");
    await page.goto("/");
    await expect(h1(page, zhText("home.noAccessTitle"))).toBeVisible();
    await page.goto("/suppliers");
    await expect(page.getByRole("heading", { name: "没有权限" })).toBeVisible();
    await expect(page.getByText("请联系站长授予权限。")).toBeVisible();
  });
});

type Held = { entered: Promise<void>; release: () => void };

// hold stops one request once it has entered the route, and says so, so a test reads the page only after the layer it
// means to check is the one on screen. Releasing it lets the request go on to the mock or the network.
async function hold(page: Page, pattern: string): Promise<Held> {
  let release = () => {};
  let entered = () => {};
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const reached = new Promise<void>((resolve) => {
    entered = resolve;
  });
  await page.route(pattern, async (route) => {
    entered();
    await held;
    await route.fallback();
  });
  return { entered: reached, release: () => release() };
}

test.describe("the four states of a view of data", () => {
  // A page area that is waiting still has a level-one heading. The three layers are held in load order, and each is
  // read only after the layer before it has left the screen, so the heading read is the one the layer under test owns.
  // Without that order the gate's loading heading could satisfy a later assertion before its request was even made
  // (#363). The requests are held until the heading is read, so this needs no sleep.
  test("the session, the page chunk and the page data each keep a level-one heading while they wait", async ({
    page,
  }) => {
    const state = await mockAdmin(page);
    signedInAs(state, "mfa");
    const session = await hold(page, "**/api/admin/v1/session");
    const chunk = await hold(page, "**/assets/model-*.js");
    const data = await hold(page, "**/api/admin/v1/models/*");
    const model = state.models[0];
    if (!model) {
      throw new Error("the mock has no model");
    }

    await page.goto(`/models/${model.id}`);

    await session.entered;
    await expect(page.getByRole("heading", { level: 1 })).toHaveText(words("state.loading"));
    session.release();
    await page
      .getByRole("link", { name: zhText("nav.models") })
      .first()
      .waitFor();

    await chunk.entered;
    await expect(page.getByRole("heading", { level: 1 })).toHaveText(words("state.loading"));
    chunk.release();

    await data.entered;
    await expect(page.getByRole("heading", { level: 1 })).toHaveText(words("state.loading"));
    data.release();

    await expect(h1(page, model.slug)).toBeVisible();
  });

  test("loading, empty and failing lists say what to do", async ({ page }) => {
    const state = await mockAdmin(page, "empty");
    signedInAs(state, "mfa");
    await page.goto("/users");
    await expect(page.getByRole("heading", { name: zhText("users.empty.title") })).toBeVisible();
    await page.getByRole("link", { name: zhText("nav.suppliers") }).click();
    await expect(page.getByRole("heading", { name: zhText("suppliers.empty.title") })).toBeVisible();
    await page.getByRole("link", { name: zhText("nav.mappings") }).click();
    await expect(page.getByRole("heading", { name: zhText("mappings.empty.title") })).toBeVisible();
    await page.getByRole("link", { name: zhText("nav.pricing") }).click();
    await expect(page.getByRole("heading", { name: zhText("pricing.empty.title") })).toBeVisible();
  });

  test("an address of something that is not there names the page so and offers home, not a retry", async ({ page }) => {
    const state = await mockAdmin(page);
    signedInAs(state, "mfa");
    for (const path of [`/accounts/${acct(999)}`, `/suppliers/${uuid(2, 999)}`, `/models/${uuid(5, 999)}`]) {
      await page.goto(path);
      await expect(h1(page, "找不到要查看的内容")).toBeVisible();
      await expect(page).toHaveTitle("找不到要查看的内容 · Axiom");
      await expect(page.getByRole("button", { name: "重试" })).toHaveCount(0);
      await expect(page.getByRole("link", { name: "回到首页" })).toBeVisible();
    }
  });

  test("a server failure is retried, then shows its request ID, and retrying asks again", async ({ page }) => {
    const state = await mockAdmin(page);
    signedInAs(state, "mfa");
    let failing = true;
    await page.route("**/api/admin/v1/users*", (route) =>
      failing
        ? route.fulfill({
            status: 500,
            headers: { "X-Request-ID": "req-admin-7" },
            json: { error: { code: "internal", message: "internal" } },
          })
        : route.fallback(),
    );
    await page.goto("/users");
    await expect(page.getByText("req-admin-7")).toBeVisible({ timeout: 15_000 });
    failing = false;
    await page.getByRole("button", { name: "重试" }).click();
    await expect(page.getByText("user1@example.com")).toBeVisible();
  });
});
