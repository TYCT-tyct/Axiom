import { expect, type Page, test } from "@playwright/test";
import { mockConsole, mockConsoleTab, signedInAs } from "./console-api.ts";
import { knownEmail, mfaEmail } from "./mock-auth.ts";
import { hold, locked, pressAt, signIn } from "./sign-in.ts";

// One request at a time on the sign-in pages (console/src/auth/attempt.ts): while one is out the page takes nothing
// but its cancel, a request that may set the session's cookie is followed whatever became of the page, and the page
// shows the session the cookie holds.

test.use({ timezoneId: "Asia/Shanghai" });

// The page reads the session again, as it does when the tab is shown again.
const refocus = (page: Page) => page.evaluate(() => window.dispatchEvent(new Event("visibilitychange")));

// The browser's passkey prompt, answered when the test says: a credential the mocked server takes, or nothing while
// it waits. Like the browser's, it is closed by the signal of the attempt it belongs to, which it counts.
async function stubPasskeyPrompt(page: Page) {
  await page.addInitScript(() => {
    const w = window as unknown as { passkeyAnswer?: () => void; passkeyAsked?: number; passkeyClosed?: number };
    w.passkeyAsked = 0;
    w.passkeyClosed = 0;
    (navigator.credentials as unknown as { get: (o?: CredentialRequestOptions) => Promise<unknown> }).get = (options) =>
      new Promise((resolve, reject) => {
        w.passkeyAsked = (w.passkeyAsked ?? 0) + 1;
        options?.signal?.addEventListener("abort", () => {
          w.passkeyClosed = (w.passkeyClosed ?? 0) + 1;
          reject(new DOMException("aborted", "AbortError"));
        });
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
    closed: () => page.evaluate(() => (window as unknown as { passkeyClosed: number }).passkeyClosed),
    answer: () => page.evaluate(() => (window as unknown as { passkeyAnswer: () => void }).passkeyAnswer()),
  };
}

const challenge = { flow_id: "flow-1", public_key: { publicKey: { challenge: "AAAA", rpId: "localhost" } } };

test.describe("one request at a time", () => {
  test("a passkey prompt holds the page until it is cancelled, which closes the prompt and signs nobody in", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    const prompt = await stubPasskeyPrompt(page);
    await page.route("**/api/console/v1/auth/passkeys/begin", (route) => route.fulfill({ json: challenge }));
    await page.goto("/console/sign-in");
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect.poll(prompt.asked).toBe(1);
    expect(await locked(page)).toBe(true);
    // the cancel takes the focus the locked control lost
    await expect(page.getByRole("button", { name: "取消" })).toBeFocused();
    await pressAt(page.locator("form .lg-note a"));
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    await page.getByRole("button", { name: "取消" }).click();
    expect(await prompt.closed()).toBe(1);
    await expect(page.getByRole("button", { name: "继续", exact: true })).toBeEnabled();
    await page.getByRole("link", { name: "注册" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "注册" })).toBeVisible();
    expect(state.log.filter((line) => line.startsWith("POST /auth/passkeys/finish"))).toEqual([]);
    expect(state.level).toBe("none");
  });

  for (const step of ["sign-in", "two-step"] as const) {
    test(`a passkey whose answer was sent (${step}) can no longer be cancelled`, async ({ page }) => {
      const state = await mockConsole(page);
      state.site = { ...state.site, passkeys: true };
      const prompt = await stubPasskeyPrompt(page);
      const { held, release } = hold();
      const path = step === "sign-in" ? "passkeys" : "passkeys/mfa";
      await page.route(`**/api/console/v1/auth/${path}/begin`, (route) => route.fulfill({ json: challenge }));
      await page.route(new RegExp(`/api/console/v1/auth/${path}/finish`), async (route) => {
        await held;
        signedInAs(state, "mfa");
        await route.fulfill({ json: { status: "signed_in", csrf_token: state.csrf } });
      });
      await page.goto("/console/");
      if (step === "two-step") await signIn(page, mfaEmail);
      await page.getByRole("button", { name: "使用通行密钥" }).click();
      await expect.poll(prompt.asked).toBe(1);
      await expect(page.getByRole("button", { name: "取消" })).toBeVisible();
      const sent = page.waitForRequest(new RegExp(`/api/console/v1/auth/${path}/finish`));
      await prompt.answer();
      await sent;
      // the answer may set the cookie: the page stays held until it comes, with nothing to cancel
      await expect(page.getByRole("button", { name: "取消" })).toHaveCount(0);
      expect(await locked(page)).toBe(true);
      release();
      await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
    });
  }

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

  test("a sign-in still out holds the sign-in page the visitor comes back to", async ({ page }) => {
    const state = await mockConsole(page);
    const { held, release } = hold();
    await page.route("**/api/console/v1/auth/sign-in", async (route) => {
      await held;
      await route.fallback();
    });
    await page.goto("/console/register");
    await page.getByRole("link", { name: "登录", exact: true }).click();
    // registration has an email field too: fill only once the sign-in page has taken its place
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    await signIn(page);
    // back to registration and forward again: the sign-in page is a new one, and the request is still out
    await page.goBack();
    await expect(page.getByRole("heading", { level: 1, name: "注册" })).toBeVisible();
    await page.goForward();
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    expect(await locked(page)).toBe(true);
    await expect(page.locator(".lg-btn.primary")).toBeDisabled();
    await page.keyboard.press("Enter");
    release();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
    expect(state.log.filter((line) => line === "POST /auth/sign-in")).toHaveLength(1);
  });

  // Playwright cannot act on a page while a navigation of it is under way, nor after one the browser dropped (it waits
  // for the next document), so the provider's address here is this page's own with a fragment: the browser goes
  // nowhere, which is where a stopped or stalled navigation leaves the visitor. The cancel's window.stop() is counted,
  // not seen stopping a navigation.
  test("a page that sent the visitor to a provider holds until it leaves or the visitor cancels", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, oauth_providers: ["github"] };
    await page.addInitScript(() => {
      const w = window as unknown as { stops: number };
      w.stops = 0;
      window.stop = () => {
        w.stops += 1;
      };
    });
    await page.route("**/api/console/v1/auth/oauth/start", (route) =>
      route.fulfill({ json: { authorize_url: `${route.request().headers().referer}#provider` } }),
    );
    await page.clock.install();
    await page.goto("/console/keys");
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByRole("button", { name: "使用 GitHub 继续" }).click();
    await expect(page).toHaveURL(/#provider$/);
    // however long the page stays, it does not guess the browser has given up
    await page.clock.fastForward(10_000);
    expect(await locked(page)).toBe(true);
    await pressAt(page.locator(".lg-btn.primary"));
    await expect(page.locator("#sign-in-password")).toHaveCount(0);
    await page.getByRole("button", { name: "取消" }).click();
    expect(await page.evaluate(() => (window as unknown as { stops: number }).stops)).toBe(1);
    // where the visitor was going is forgotten with the trip, so no later visit to the root is sent there
    expect(await page.evaluate(() => sessionStorage.getItem("axiom:oauth-next"))).toBeNull();
    await signIn(page);
    await expect(page.getByRole("heading", { level: 1, name: "API Keys" })).toBeVisible();
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
      // taken once: the root, opened again, is the overview
      await page.goto("/console/");
      await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
    });
  }

  test("a provider's failed return is said once, not again after a later attempt is cancelled", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    await stubPasskeyPrompt(page);
    await page.route("**/api/console/v1/auth/passkeys/begin", (route) => route.fulfill({ json: challenge }));
    await page.goto("/console/sign-in?oauth=failed");
    await expect(page.getByRole("alert")).toHaveText(/这次登录没有完成/);
    await expect(page).not.toHaveURL(/oauth=failed/);
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await page.getByRole("button", { name: "取消" }).click();
    await expect(page.getByRole("button", { name: "继续", exact: true })).toBeEnabled();
    await expect(page.getByRole("alert")).toHaveCount(0);
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
    const otherAccount = page.getByRole("button", { name: "换一个账户登录" });
    const passkey = page.getByRole("button", { name: "使用通行密钥" });
    const [other, key] = [await otherAccount.boundingBox(), await passkey.boundingBox()];
    await page.getByRole("button", { name: "验证" }).click();
    expect(await locked(page)).toBe(true);
    await expect(page.locator("form")).toHaveAttribute("aria-busy", "true");
    // both would answer for the same pending session: which one won would depend on the order of their answers
    for (const box of [other, key]) await page.mouse.click((box?.x ?? 0) + 4, (box?.y ?? 0) + 4);
    release();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
    expect(
      state.log.filter((line) => line === "POST /auth/sign-out" || line.startsWith("POST /auth/passkeys")),
    ).toEqual([]);
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

  test("five wrong codes end the pending sign-in: it is signed out, and the sign-in page says why", async ({
    page,
  }) => {
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
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    await expect(page.getByText("上一次登录已经失效，请重新登录。")).toBeVisible();
    expect(new URL(page.url()).searchParams.get("next")).toBe("/keys");
    expect(state.level).toBe("none");
    // the cookie holds no session: a reload is the sign-in page too, not a step that cannot be finished
    await page.reload();
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
  });

  test("a new pending sign-in of the same person, in another tab, starts this tab's step afresh", async ({
    page,
    context,
  }) => {
    const state = await mockConsole(page);
    await page.goto("/console/");
    await signIn(page, mfaEmail);
    await page.getByLabel("验证码或恢复码").fill("000000");
    await page.getByRole("button", { name: "验证" }).click();
    await expect(page.getByRole("alert")).toHaveText(/验证码不对/);
    // the same person signs in again in another tab of this browser: the cookie names a new pending session
    const other = await context.newPage();
    await mockConsoleTab(other, state);
    await other.goto("/console/");
    await other.getByRole("button", { name: "换一个账户登录" }).click();
    await signIn(other, mfaEmail);
    await expect(other.getByLabel("验证码或恢复码")).toBeVisible();
    await page.bringToFront();
    await refocus(page);
    await expect(page.getByRole("alert")).toHaveCount(0);
    await expect(page.getByLabel("验证码或恢复码")).toHaveValue("");
  });

  test("a passkey that is out keeps the code waiting", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    const prompt = await stubPasskeyPrompt(page);
    await page.route("**/api/console/v1/auth/passkeys/mfa/begin", (route) => route.fulfill({ json: challenge }));
    await page.goto("/console/");
    await signIn(page, mfaEmail);
    await page.getByLabel("验证码或恢复码").fill(state.user.mfaCode);
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect.poll(prompt.asked).toBe(1);
    expect(await locked(page)).toBe(true);
    await pressAt(page.locator(".lg-btn.primary"));
    expect(state.log.filter((line) => line === "POST /auth/mfa")).toEqual([]);
  });

  test("a second step that passed but whose session could not be read offers only that read again", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    // the session, once verified, cannot be read, until the test says
    let unreadable = true;
    await page.route("**/api/console/v1/session", (route) => {
      if (state.level !== "mfa" || !unreadable) return route.fallback();
      return route.fulfill({ status: 503, json: { error: { code: "unavailable", message: "unavailable" } } });
    });
    await page.goto("/console/");
    await signIn(page, mfaEmail);
    await page.getByLabel("验证码或恢复码").fill(state.user.mfaCode);
    await page.getByRole("button", { name: "验证" }).click();
    await expect(page.getByText("验证已通过，但没能读取登录状态。")).toBeVisible();
    // the step is over: no code to send again, no sign-out of the session it started
    await expect(page.getByLabel("验证码或恢复码")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "换一个账户登录" })).toHaveCount(0);
    unreadable = false;
    await page.getByRole("button", { name: "重试" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
    expect(state.log.filter((line) => line === "POST /auth/mfa" || line === "POST /auth/sign-out")).toEqual([
      "POST /auth/mfa",
    ]);
  });

  test("a pending sign-in that expired while its step was open ends on the sign-in page, which says why", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    await page.goto("/console/");
    await signIn(page, mfaEmail);
    await page.getByLabel("验证码或恢复码").fill(state.user.mfaCode);
    // the pending session runs out on the server: the code and the session read are refused alike (401)
    state.level = "none";
    await page.getByRole("button", { name: "验证" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    await expect(page.getByText("上一次登录已经失效，请重新登录。")).toBeVisible();
    expect(state.log.filter((line) => line === "POST /auth/sign-out")).toEqual([]);
  });

  test("a spent challenge whose sign-out fails says so, with no code to send, and the sign-out can be tried again", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    // the first sign-out fails; the one tried again goes through
    let failing = true;
    await page.route("**/api/console/v1/auth/sign-out", async (route) => {
      if (!failing) return route.fallback();
      failing = false;
      return route.fulfill({ status: 503, json: { error: { code: "unavailable", message: "unavailable" } } });
    });
    await page.goto("/console/");
    await signIn(page, mfaEmail);
    const code = page.getByLabel("验证码或恢复码");
    for (let i = 0; i < 5; i++) {
      await code.fill("000000");
      await page.getByRole("button", { name: "验证" }).click();
      await expect(page.getByRole("alert")).toBeVisible();
    }
    await code.fill(state.user.mfaCode);
    await page.getByRole("button", { name: "验证" }).click();
    await expect(page.getByText("这次登录已经失效，请重新登录。")).toBeVisible();
    await expect(page.getByRole("alert")).toContainText("你仍在登录状态");
    await expect(code).toHaveCount(0);
    await page.getByRole("button", { name: "重新登录" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    await expect(page.getByText("上一次登录已经失效，请重新登录。")).toBeVisible();
  });
});
