import { expect, type Page, test } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";
import { knownEmail, mfaEmail } from "./mock-auth.ts";
import { hold, signIn } from "./sign-in.ts";

// One request at a time on the sign-in pages (console/src/auth/attempt.ts): while one is out the page takes nothing
// but its cancel, a request that may set the session's cookie is followed whatever became of the page, and the page
// shows the session the cookie holds.

test.use({ timezoneId: "Asia/Shanghai" });

// The page reads the session again, as it does when the tab is shown again.
const refocus = (page: Page) => page.evaluate(() => window.dispatchEvent(new Event("visibilitychange")));

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

test.describe("one request at a time", () => {
  test("a passkey prompt holds the page until it is cancelled, which sends nothing and signs nobody in", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    const prompt = await stubPasskeyPrompt(page);
    await page.route("**/api/console/v1/auth/passkeys/begin", (route) => route.fulfill({ json: challenge }));
    await page.goto("/console/sign-in");
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect.poll(prompt.asked).toBe(1);
    await page.getByRole("link", { name: "注册" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    await page.getByRole("button", { name: "取消" }).click();
    await expect(page.getByRole("button", { name: "继续", exact: true })).toBeEnabled();
    // the prompt was closed with the request: an answer from it now goes nowhere
    await prompt.answer().catch(() => {});
    await page.getByRole("link", { name: "注册" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "注册" })).toBeVisible();
    expect(state.log.filter((line) => line.startsWith("POST /auth/passkeys/finish"))).toEqual([]);
    expect(state.level).toBe("none");
  });

  test("a passkey sign-in already sent when the visitor leaves is followed: the page shows the session it made", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    const prompt = await stubPasskeyPrompt(page);
    const { held, release } = hold();
    await page.route("**/api/console/v1/auth/passkeys/begin", (route) => route.fulfill({ json: challenge }));
    await page.route(/\/api\/console\/v1\/auth\/passkeys\/finish/, async (route) => {
      await held;
      signedInAs(state);
      await route.fulfill({ json: { status: "signed_in", csrf_token: state.csrf } });
    });
    await page.goto("/console/register");
    await page.getByRole("link", { name: "登录", exact: true }).click();
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect.poll(prompt.asked).toBe(1);
    const sent = page.waitForRequest(/\/api\/console\/v1\/auth\/passkeys\/finish/);
    await prompt.answer();
    await sent;
    // the browser's own way back, which no lock on the page holds
    await page.goBack();
    await expect(page.getByRole("heading", { level: 1, name: "注册" })).toBeVisible();
    release();
    // the server set its cookie: the page follows the session, not the page the visitor had gone to
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  });

  // Playwright cannot act on a page while a navigation of it is under way, nor after one the browser dropped (it waits
  // for the next document), so the provider's address here is this page's own with a fragment: the browser goes
  // nowhere, which is where a stopped or stalled navigation leaves the visitor. That the cancel also stops a navigation
  // still under way (window.stop) is the browser's, and not tested here.
  test("a page that sent the visitor to a provider holds until it leaves or the visitor cancels", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, oauth_providers: ["github"] };
    await page.route("**/api/console/v1/auth/oauth/start", (route) =>
      route.fulfill({ json: { authorize_url: `${new URL(route.request().url()).origin}/console/sign-in#provider` } }),
    );
    await page.clock.install();
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByRole("button", { name: "使用 GitHub 继续" }).click();
    await expect(page).toHaveURL(/#provider$/);
    // however long the page stays, it does not guess the browser has given up
    await page.clock.fastForward(10_000);
    await page.getByLabel("邮箱").press("Enter");
    await expect(page.getByLabel("密码", { exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "继续", exact: true })).toBeDisabled();
    await page.getByRole("button", { name: "取消" }).click();
    await signIn(page);
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  });

  for (const outcome of ["signs in", "fails"] as const) {
    test(`a provider's sign-in that ${outcome} comes back to where the visitor was going`, async ({ page }) => {
      const state = await mockConsole(page);
      state.site = { ...state.site, oauth_providers: ["github"] };
      await page.route("**/api/console/v1/auth/oauth/start", (route) =>
        route.fulfill({ json: { authorize_url: `${new URL(route.request().url()).origin}/console/provider-away` } }),
      );
      // the provider and the server's callback in one: its 303 to the console's root, or to the sign-in page
      await page.route("**/console/provider-away", (route) => {
        if (outcome === "signs in") signedInAs(state);
        const location = outcome === "signs in" ? "/console/" : "/console/sign-in?oauth=failed";
        return route.fulfill({ status: 303, headers: { location } });
      });
      await page.goto("/console/keys");
      await page.getByLabel("邮箱").fill(knownEmail);
      await page.getByRole("button", { name: "使用 GitHub 继续" }).click();
      if (outcome === "fails") {
        await expect(page.getByRole("alert")).toHaveText(/这次登录没有完成/);
        expect(new URL(page.url()).searchParams.get("next")).toBe("/keys");
        await signIn(page);
      }
      await expect(page.getByRole("heading", { level: 1, name: "API Keys" })).toBeVisible();
    });
  }

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

  test("a code that is out keeps the passkey and the sign-out waiting", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    const { held, release } = hold();
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
    // both would answer for the same pending session: which one won would depend on the order of their answers
    await page.getByRole("button", { name: "换一个账户登录" }).click();
    release();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
    expect(state.log.filter((line) => line === "POST /auth/sign-out")).toEqual([]);
  });

  test("a session read still out when a sign-in sets its cookie is not the session the page shows", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    await page.goto("/console/sign-in");
    await expect(page.getByLabel("邮箱")).toBeVisible();
    // a read of the session from before the sign-in, answered as it was then: nobody
    const { held, release } = hold();
    let stale = true;
    await page.route("**/api/console/v1/session", async (route) => {
      if (!stale) return route.fallback();
      stale = false;
      await held;
      await route.fulfill({ status: 401, json: { error: { code: "unauthenticated", message: "unauthenticated" } } });
    });
    const asked = page.waitForRequest("**/api/console/v1/session");
    await refocus(page);
    await asked;
    await signIn(page);
    await expect.poll(() => state.level).toBe("password");
    release();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  });

  test("five wrong codes end the pending sign-in: a right one then offers only to sign in again", async ({ page }) => {
    const state = await mockConsole(page);
    await page.goto("/console/keys");
    await signIn(page, mfaEmail);
    const code = page.getByLabel("验证码或恢复码");
    for (let i = 0; i < 5; i++) {
      await code.fill("000000");
      await page.getByRole("button", { name: "验证" }).click();
      await expect(page.getByRole("alert")).toBeVisible();
    }
    await code.fill(state.user.mfaCode);
    await page.getByRole("button", { name: "验证" }).click();
    await expect(page.getByText(/这次登录已经失效/)).toBeVisible();
    await expect(code).toHaveCount(0);
    await page.getByRole("button", { name: "重新登录" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    expect(new URL(page.url()).searchParams.get("next")).toBe("/keys");
  });

  test("a new pending sign-in of the same person starts its step afresh", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    await page.route("**/api/console/v1/auth/passkeys/mfa/begin", (route) =>
      route.fulfill({ status: 401, json: { error: { code: "unauthenticated", message: "unauthenticated" } } }),
    );
    await page.goto("/console/");
    await signIn(page, mfaEmail);
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect(page.getByText(/这次登录已经失效/)).toBeVisible();
    // the same person signs in again in another tab: the cookie names a new pending session, with its own token
    state.csrf = "csrf-another-tab";
    await refocus(page);
    await expect(page.getByLabel("验证码或恢复码")).toBeVisible();
    await expect(page.getByRole("button", { name: "重新登录" })).toHaveCount(0);
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
