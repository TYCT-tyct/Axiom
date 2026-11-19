import { expect, type Page, test } from "@playwright/test";
import { contrast } from "../packages/ui/src/brand.ts";
import { failOnce, mockConsole, signedInAs } from "./console-api.ts";
import { demoDocuments } from "./console-data.ts";
import { knownEmail, knownPassword, mfaEmail } from "./mock-auth.ts";
import { signIn } from "./sign-in.ts";

// The sign-in pages as the Login board draws them, against the mocked API: the steps, one sign-in attempt at a time,
// and the board's parts as the console sets them (the flows themselves are console-auth.spec.ts's).

test.use({ timezoneId: "Asia/Shanghai" });

test.describe("the sign-in steps", () => {
  test("while a provider's start is out, no other way in on the step can be taken", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, oauth_providers: ["github"], passkeys: true };
    let answer!: () => void;
    const held = new Promise<void>((resolve) => (answer = resolve));
    await page.route("**/api/console/v1/auth/oauth/start", async (route) => {
      await held;
      await route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } });
    });
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByRole("button", { name: "使用 GitHub 继续" }).click();
    await expect(page.getByRole("button", { name: "继续", exact: true })).toBeDisabled();
    await expect(page.getByRole("button", { name: "使用通行密钥" })).toBeDisabled();
    await expect(page.getByRole("button", { name: "使用 GitHub 继续" })).toBeDisabled();
    // Enter submits the form, which the attempt keeps on this step too
    await page.getByLabel("邮箱").press("Enter");
    await expect(page.getByLabel("密码", { exact: true })).toHaveCount(0);
    answer();
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(page.getByRole("button", { name: "继续", exact: true })).toBeEnabled();
  });

  test("an address the server refused stays on the email step until it is changed", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/sign-in");
    await signIn(page, "a");
    await expect(page.getByRole("alert")).toHaveText(/不像一个邮箱地址/);
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await expect(page.getByLabel("密码", { exact: true })).toHaveCount(0);
    await expect(page.getByRole("alert")).toHaveText(/不像一个邮箱地址/);
    await page.getByLabel("邮箱").fill(knownEmail);
    await expect(page.getByRole("alert")).toHaveCount(0);
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await expect(page.getByLabel("密码", { exact: true })).toBeVisible();
  });

  test("a pending sign-in that is over offers only to sign in again, and a sign-out that fails can be retried", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    await page.route("**/api/console/v1/auth/passkeys/mfa/begin", (route) =>
      route.fulfill({ status: 401, json: { error: { code: "unauthenticated", message: "unauthenticated" } } }),
    );
    await page.goto("/console/keys");
    await signIn(page, mfaEmail);
    await expect(page.getByRole("heading", { level: 1, name: "两步验证" })).toBeVisible();
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect(page.getByText(/这次登录已经失效/)).toBeVisible();
    // no code or passkey can finish it any more
    await expect(page.getByLabel("验证码或恢复码")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "使用通行密钥" })).toHaveCount(0);
    await failOnce(page, "**/api/console/v1/auth/sign-out");
    await page.getByRole("button", { name: "重新登录" }).click();
    await expect(page.getByRole("alert")).toContainText("你仍在登录状态");
    await page.getByRole("button", { name: "重新登录" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    expect(new URL(page.url()).searchParams.get("next")).toBe("/keys");
  });

  test("a passkey that fails otherwise can be tried again on the step", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    await page.route("**/api/console/v1/auth/passkeys/mfa/begin", (route) =>
      route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } }),
    );
    await page.goto("/console/");
    await signIn(page, mfaEmail);
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(page.getByRole("button", { name: "使用通行密钥" })).toBeEnabled();
  });

  test("while a sign-in is out the step cannot be left, so its answer signs in the address it was for", async ({
    page,
  }) => {
    await mockConsole(page);
    let answer!: () => void;
    const held = new Promise<void>((resolve) => (answer = resolve));
    await page.route("**/api/console/v1/auth/sign-in", async (route) => {
      await held;
      await route.fallback();
    });
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await page.getByLabel("密码", { exact: true }).fill(knownPassword);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await expect(page.getByRole("button", { name: "换个邮箱" })).toHaveCount(0);
    answer();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  });

  test("switching the language keeps what was typed", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await page.getByLabel("密码", { exact: true }).fill(knownPassword);
    await page.getByRole("link", { name: "EN", exact: true }).click();
    await expect(page.getByRole("heading", { level: 1, name: "Enter your password" })).toBeVisible();
    await expect(page.getByLabel("Password", { exact: true })).toHaveValue(knownPassword);
  });

  test("the latest way in says why it failed, not an earlier one", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, oauth_providers: ["github"], passkeys: true };
    await page.route("**/api/console/v1/auth/oauth/start", (route) =>
      route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } }),
    );
    await page.route("**/api/console/v1/auth/passkeys/begin", (route) =>
      route.fulfill({
        status: 429,
        headers: { "Retry-After": "30" },
        json: { error: { code: "rate_limited", message: "rate_limited" } },
      }),
    );
    await page.goto("/console/sign-in?oauth=failed");
    await expect(page.getByRole("alert")).toHaveText(/这次登录没有完成/);
    await page.getByRole("button", { name: "使用 GitHub 继续" }).click();
    await expect(page.getByRole("alert")).not.toHaveText(/这次登录没有完成/);
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect(page.getByRole("alert")).toHaveText(/30 秒/);
  });

  test("a long address breaks inside the phone's form", async ({ page }) => {
    await mockConsole(page);
    await page.setViewportSize({ width: 360, height: 800 });
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(`${"w".repeat(64)}@example.com`);
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await expect(page.getByLabel("密码", { exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(360);
  });

  test("the site's brand colour is the focus ring of the sign-in fields", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, brand_color: "#335ddf" };
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").focus();
    const { ring, accent } = await page.evaluate(() => {
      const field = document.querySelector(".lg-field") as HTMLElement;
      const probe = document.createElement("i");
      probe.style.color = "var(--accentT)";
      field.append(probe);
      const accent = getComputedStyle(probe).color;
      probe.remove();
      return { ring: getComputedStyle(field).outlineColor, accent };
    });
    expect(ring).toBe(accent);
    expect(ring).not.toBe("rgb(7, 151, 185)");
  });
});

// The browser's passkey prompt, answered when the test says: a credential the mocked server takes, or nothing while
// it waits. Like the browser's, it is closed by the signal of the attempt it belongs to.
async function stubPasskeyPrompt(page: Page) {
  await page.addInitScript(() => {
    const w = window as unknown as { passkeyAnswer?: () => void; passkeyAsked?: number };
    w.passkeyAsked = 0;
    (navigator.credentials as unknown as { get: (o?: CredentialRequestOptions) => Promise<unknown> }).get = (options) =>
      new Promise((resolve, reject) => {
        w.passkeyAsked = (w.passkeyAsked ?? 0) + 1;
        options?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        w.passkeyAnswer = () => {
          const bytes = new Uint8Array([1, 2, 3]).buffer;
          const credential = Object.create(PublicKeyCredential.prototype);
          const fields = {
            id: "AQID",
            rawId: bytes,
            type: "public-key",
            authenticatorAttachment: "platform",
            response: { clientDataJSON: bytes, authenticatorData: bytes, signature: bytes, userHandle: bytes },
            getClientExtensionResults: () => ({}),
          };
          for (const [key, value] of Object.entries(fields)) Object.defineProperty(credential, key, { value });
          resolve(credential);
        };
      });
  });
  return {
    asked: () => page.evaluate(() => (window as unknown as { passkeyAsked: number }).passkeyAsked),
    answer: () => page.evaluate(() => (window as unknown as { passkeyAnswer: () => void }).passkeyAnswer()),
  };
}

const challenge = { flow_id: "flow-1", public_key: { publicKey: { challenge: "AAAA", rpId: "localhost" } } };

test.describe("one sign-in attempt at a time", () => {
  test("a passkey prompt left for registration sends nothing, and signs nobody in", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    const prompt = await stubPasskeyPrompt(page);
    const finished: string[] = [];
    await page.route("**/api/console/v1/auth/passkeys/begin", (route) => route.fulfill({ json: challenge }));
    await page.route(/\/api\/console\/v1\/auth\/passkeys\/finish/, (route) => {
      finished.push(route.request().url());
      return route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } });
    });
    await page.goto("/console/sign-in");
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect.poll(prompt.asked).toBe(1);
    await page.getByRole("link", { name: "注册" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "注册" })).toBeVisible();
    await prompt.answer();
    await page.reload();
    await expect(page.getByRole("heading", { level: 1, name: "注册" })).toBeVisible();
    expect(finished).toEqual([]);
    expect(state.level).toBe("none");
  });

  test("a passkey sign-in already sent when the visitor leaves is followed: the page shows the session it made", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    const prompt = await stubPasskeyPrompt(page);
    let answer!: () => void;
    const held = new Promise<void>((resolve) => (answer = resolve));
    await page.route("**/api/console/v1/auth/passkeys/begin", (route) => route.fulfill({ json: challenge }));
    await page.route(/\/api\/console\/v1\/auth\/passkeys\/finish/, async (route) => {
      await held;
      signedInAs(state);
      await route.fulfill({ json: { status: "signed_in", csrf_token: state.csrf } });
    });
    await page.goto("/console/sign-in");
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect.poll(prompt.asked).toBe(1);
    const sent = page.waitForRequest(/\/api\/console\/v1\/auth\/passkeys\/finish/);
    await prompt.answer();
    await sent;
    await page.getByRole("link", { name: "注册" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "注册" })).toBeVisible();
    answer();
    // the server set its cookie: the page follows the session, not the page the visitor had gone to
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  });

  // A stopped navigation is not one a test can reproduce reliably here (the browser's stop races the request, and an
  // aborted top-level navigation is not reported): the attempt's end on a stall and on a restore is attempt.test.tsx's.
  test("a provider's page the visitor comes back from leaves the password free to use", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, oauth_providers: ["github"] };
    await page.route("**/api/console/v1/auth/oauth/start", (route) =>
      route.fulfill({ json: { authorize_url: `${new URL(route.request().url()).origin}/console/provider-away` } }),
    );
    await page.route("**/console/provider-away", (route) =>
      route.fulfill({ contentType: "text/html", body: "<title>provider</title><h1>provider</h1>" }),
    );
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByRole("button", { name: "使用 GitHub 继续" }).click();
    await expect(page).toHaveURL(/provider-away/);
    await page.goBack();
    await expect(page.getByRole("button", { name: "继续", exact: true })).toBeEnabled({ timeout: 5_000 });
    await signIn(page);
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  });

  test("a code that is out keeps the passkey waiting", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    let answer!: () => void;
    const held = new Promise<void>((resolve) => (answer = resolve));
    await page.route("**/api/console/v1/auth/mfa", async (route) => {
      await held;
      await route.fallback();
    });
    await page.goto("/console/");
    await signIn(page, mfaEmail);
    await page.getByLabel("验证码或恢复码").fill(state.user.mfaCode);
    await page.getByRole("button", { name: "验证" }).click();
    await expect(page.getByRole("button", { name: "使用通行密钥" })).toBeDisabled();
    await expect(page.locator("form")).toHaveAttribute("aria-busy", "true");
    answer();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  });

  test("a passkey that is out keeps the code waiting", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    const prompt = await stubPasskeyPrompt(page);
    await page.route("**/api/console/v1/auth/passkeys/mfa/begin", (route) => route.fulfill({ json: challenge }));
    await page.goto("/console/");
    await signIn(page, mfaEmail);
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect.poll(prompt.asked).toBe(1);
    await page.getByLabel("验证码或恢复码").fill(state.user.mfaCode);
    await expect(page.getByRole("button", { name: "验证" })).toBeDisabled();
    await page.getByLabel("验证码或恢复码").press("Enter");
    expect(state.log.filter((line) => line === "POST /auth/mfa")).toEqual([]);
  });
});

test.describe("the sign-in page's parts", () => {
  test("a button waiting for the visitor looks disabled; one waiting for the server says so", async ({ page }) => {
    const state = await mockConsole(page);
    state.legal = demoDocuments();
    signedInAs(state);
    let answer!: () => void;
    const held = new Promise<void>((resolve) => (answer = resolve));
    await page.route("**/api/console/v1/legal/accept", async (route) => {
      await held;
      await route.fallback();
    });
    await page.goto("/console/consent");
    const submit = page.getByRole("button", { name: "同意并继续" });
    await expect(submit).toBeDisabled();
    const look = () =>
      submit.evaluate((button) => {
        const field = getComputedStyle(button.closest(".lg") as HTMLElement)
          .getPropertyValue("--field")
          .trim();
        const probe = document.createElement("i");
        probe.style.color = field;
        button.append(probe);
        const fill = getComputedStyle(probe).color;
        probe.remove();
        return { background: getComputedStyle(button).backgroundColor, fill };
      });
    const disabled = await look();
    expect(disabled.background).toBe(disabled.fill);
    await expect(page.locator("form")).not.toHaveAttribute("aria-busy", "true");
    await page.getByLabel("我已阅读并同意上述服务条款和隐私政策。").check();
    expect((await look()).background).not.toBe(disabled.fill);
    await submit.click();
    await expect(page.locator("form")).toHaveAttribute("aria-busy", "true");
    answer();
    await expect(
      page
        .getByRole("heading", { level: 1, name: "API Keys" })
        .or(page.getByRole("heading", { level: 1, name: "概览" })),
    ).toBeVisible();
  });

  test("a site name of 100 characters ends before the language switch on a phone", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, name: "W".repeat(100) };
    await page.setViewportSize({ width: 360, height: 800 });
    await page.goto("/console/sign-in");
    const brand = page.locator(".lg-sea .brand");
    await expect(brand).toContainText("WWW");
    const nav = await page.locator(".lg-top").boundingBox();
    const box = await brand.boundingBox();
    expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(nav?.x ?? 0);
    const truncated = await brand
      .locator("span")
      .last()
      .evaluate((name) => name.scrollWidth > name.clientWidth);
    expect(truncated).toBe(true);
  });
});

test.describe("accepting updated documents on the board", () => {
  test("switching the language keeps the box as the form would send it", async ({ page }) => {
    const state = await mockConsole(page);
    state.legal = demoDocuments().filter((document) => document.locale === "zh");
    signedInAs(state);
    await page.goto("/console/consent");
    const box = page.getByLabel("我已阅读并同意上述服务条款和隐私政策。");
    await box.check();
    await page.getByRole("link", { name: "EN", exact: true }).click();
    await expect(
      page.getByLabel("I have read and agree to the terms of service and privacy policy above."),
    ).toBeChecked();
    await expect(page.getByRole("button", { name: "Accept and continue" })).toBeEnabled();
  });
});

// A control's edge against its own fill, as the page computes them: WCAG 2.2's 3:1 (docs/frontend.md「对比度令牌」).
const rgb = (css: string) => (css.match(/\d+(\.\d+)?/g) ?? []).slice(0, 3).map(Number) as [number, number, number];

async function edgeContrast(page: Page, selector: string) {
  const control = page.locator(selector).first();
  await expect(control).toBeVisible();
  const { edge, fill } = await control.evaluate((element) => {
    const style = getComputedStyle(element);
    return { edge: style.boxShadow.match(/rgba?\([^)]*\)/)?.[0] ?? "", fill: style.backgroundColor };
  });
  return contrast(rgb(edge), rgb(fill));
}

for (const scheme of ["light", "dark"] as const) {
  test(`the sign-in controls' edges hold 3:1 against their fill (${scheme})`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    const state = await mockConsole(page);
    // a site with other ways in, so the secondary buttons are there to measure
    state.site = { ...state.site, oauth_providers: ["github"], passkeys: true };
    await page.goto("/console/sign-in");
    expect(await edgeContrast(page, ".lg-field")).toBeGreaterThanOrEqual(3);
    expect(await edgeContrast(page, ".lg-btn:not(.primary)")).toBeGreaterThanOrEqual(3);
  });

  test(`the terms box's edge holds 3:1 against its fill (${scheme})`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    const state = await mockConsole(page);
    state.legal = demoDocuments();
    signedInAs(state);
    await page.goto("/console/consent");
    expect(await edgeContrast(page, ".lg-consent .box")).toBeGreaterThanOrEqual(3);
  });
}
