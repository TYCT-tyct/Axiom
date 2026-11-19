import { expect, type Page, test } from "@playwright/test";
import { contrast } from "../packages/ui/src/brand.ts";
import { failOnce, mockConsole, signedInAs } from "./console-api.ts";
import { demoDocuments } from "./console-data.ts";
import { knownEmail, knownPassword, mfaEmail, passkeyEmail } from "./mock-auth.ts";
import { hold, locked, pressAt, signIn } from "./sign-in.ts";

// The sign-in pages as the Login board draws them, against the mocked API: the steps and the board's parts as the console
// sets them (one request at a time is console-sign-in-requests.spec.ts's; the flows are console-auth.spec.ts's).

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
    // the form, the sea's links and the language switch are inert: no press, typing or focus reaches them
    expect(await locked(page)).toBe(true);
    await expect(page.locator(".lg-btn")).toHaveCount(3);
    for (const button of await page.locator(".lg-btn").all()) await expect(button).toBeDisabled();
    await pressAt(page.locator(".lg-btn.primary"));
    await expect(page.locator("#sign-in-password")).toHaveCount(0);
    // nor a link off the page, nor the language
    await pressAt(page.locator("form .lg-note a"));
    await pressAt(page.locator(".lg-top a").last());
    await expect(page).toHaveURL(/\/console\/sign-in/);
    expect(await page.evaluate(() => document.documentElement.lang)).toMatch(/^zh/);
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

  test("a pending sign-in that is over is signed out, and a sign-out that fails says so and can be retried", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    await page.route("**/api/console/v1/auth/passkeys/mfa/begin", (route) =>
      route.fulfill({ status: 401, json: { error: { code: "unauthenticated", message: "unauthenticated" } } }),
    );
    await failOnce(page, "**/api/console/v1/auth/sign-out");
    await page.goto("/console/keys");
    await signIn(page, mfaEmail);
    await expect(page.getByRole("heading", { level: 1, name: "两步验证" })).toBeVisible();
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect(page.getByText(/这次登录已经失效/)).toBeVisible();
    await expect(page.getByRole("alert")).toContainText("你仍在登录状态");
    // no code or passkey can finish it any more
    await expect(page.getByLabel("验证码或恢复码")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "使用通行密钥" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "重新登录" })).toBeFocused();
    await page.getByRole("button", { name: "重新登录" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "登录" })).toBeVisible();
    await expect(page.getByText("上一次登录已经失效，请重新登录。")).toBeVisible();
    expect(new URL(page.url()).searchParams.get("next")).toBe("/keys");
  });

  test("a passkey that fails otherwise can be tried again on the step, and then signs in", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    await page.addInitScript(() => {
      (navigator.credentials as unknown as { get: () => Promise<unknown> }).get = async () => {
        const bytes = new Uint8Array([1, 2, 3]).buffer;
        const credential = Object.create(PublicKeyCredential.prototype);
        const response = { clientDataJSON: bytes, authenticatorData: bytes, signature: bytes, userHandle: bytes };
        const fields = {
          id: "AQID",
          rawId: bytes,
          type: "public-key",
          authenticatorAttachment: "platform",
          response,
          getClientExtensionResults: () => ({}),
        };
        for (const [key, value] of Object.entries(fields)) Object.defineProperty(credential, key, { value });
        return credential;
      };
    });
    // the later route answers first: the first begin fails, the next is the challenge
    await page.route("**/api/console/v1/auth/passkeys/mfa/begin", (route) =>
      route.fulfill({
        json: { flow_id: "flow-1", public_key: { publicKey: { challenge: "AAAA", rpId: "localhost" } } },
      }),
    );
    await failOnce(page, "**/api/console/v1/auth/passkeys/mfa/begin");
    await page.route(/\/api\/console\/v1\/auth\/passkeys\/mfa\/finish/, (route) => {
      signedInAs(state, "mfa");
      return route.fulfill({ json: { status: "signed_in", csrf_token: state.csrf } });
    });
    await page.goto("/console/");
    await signIn(page, mfaEmail);
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect(page.getByRole("alert")).toBeVisible();
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  });

  test("a person with only a passkey is asked for it, not for a code", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, passkeys: true };
    const asked: string[] = [];
    await page.route("**/api/console/v1/auth/passkeys/mfa/begin", (route) => {
      asked.push(route.request().url());
      return route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } });
    });
    await page.goto("/console/");
    await signIn(page, passkeyEmail);
    await expect(page.getByRole("heading", { level: 1, name: "两步验证" })).toBeVisible();
    await expect(page.getByLabel("验证码或恢复码")).toHaveCount(0);
    // the step's one action, which Enter takes too
    await page.getByRole("button", { name: "使用通行密钥" }).click();
    await expect.poll(() => asked.length).toBe(1);
  });

  for (const before of ["nothing", "another address"] as const) {
    test(`the email step sends the address the field holds, written without input events over ${before}`, async ({
      page,
    }) => {
      await mockConsole(page);
      await page.goto("/console/sign-in");
      const field = page.getByLabel("邮箱");
      if (before === "another address") await field.fill("old@example.com");
      // a password manager's fill: the value changes, the page hears nothing
      await field.evaluate((input, email) => {
        (input as HTMLInputElement).value = email;
      }, knownEmail);
      await page.getByRole("button", { name: "继续", exact: true }).click();
      await expect(page.getByText(`用 ${knownEmail} 登录`)).toBeVisible();
      await page.getByLabel("密码", { exact: true }).fill(knownPassword);
      const sent = page.waitForRequest("**/api/console/v1/auth/sign-in");
      await page.getByRole("button", { name: "登录", exact: true }).click();
      expect((await sent).postDataJSON().email).toBe(knownEmail);
      await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
    });
  }

  test("an older server's session, without totp_enabled, is asked for a code as before", async ({ page }) => {
    const state = await mockConsole(page);
    state.legacySession = true;
    await page.goto("/console/");
    await signIn(page, passkeyEmail);
    await expect(page.getByLabel("验证码或恢复码")).toBeVisible();
  });

  test("an empty code is said at its field, not sent", async ({ page }) => {
    const state = await mockConsole(page);
    await page.goto("/console/");
    await signIn(page, mfaEmail);
    await page.getByRole("button", { name: "验证" }).click();
    await expect(page.getByRole("alert")).toHaveText("请填写验证码或恢复码。");
    await expect(page.getByLabel("验证码或恢复码")).toHaveAttribute("aria-invalid", "true");
    expect(state.log.filter((line) => line === "POST /auth/mfa")).toEqual([]);
  });

  test("nothing is sent before the site's settings say this is the domain to sign in on", async ({ page }) => {
    const state = await mockConsole(page);
    const { held, release } = hold();
    await page.route("**/api/console/v1/site", async (route) => {
      await held;
      await route.fallback();
    });
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByLabel("邮箱").press("Enter");
    await expect(page.locator(".lg-btn.primary")).toBeDisabled();
    await expect(page.getByLabel("密码", { exact: true })).toHaveCount(0);
    release();
    await signIn(page);
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
    expect(state.log.filter((line) => line === "POST /auth/sign-in")).toHaveLength(1);
  });

  test("another step takes the focus to its first control", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await expect(page.getByLabel("密码", { exact: true })).toBeFocused();
    await page.getByRole("button", { name: "换个邮箱" }).click();
    await expect(page.getByLabel("邮箱")).toBeFocused();
  });

  test("the gate's states on a sign-in page are set in the sign-in pages' faces", async ({ page }) => {
    await mockConsole(page);
    await page.route("**/api/console/v1/session", (route) =>
      route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } }),
    );
    await page.goto("/console/sign-in");
    const heading = page.getByRole("heading", { level: 1 });
    await expect(heading).toBeVisible();
    const family = await heading.evaluate((element) => getComputedStyle(element).fontFamily);
    expect(family).toContain('"Axiom CJK"');
    expect(family).not.toContain("Axiom CJK Console");
  });

  test("while a sign-in is out the step cannot be left nor cancelled, so its answer signs in the address it was for", async ({
    page,
  }) => {
    await mockConsole(page);
    const { held, release } = hold();
    await page.route("**/api/console/v1/auth/sign-in", async (route) => {
      await held;
      await route.fallback();
    });
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await page.getByLabel("密码", { exact: true }).fill(knownPassword);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    expect(await locked(page)).toBe(true);
    await pressAt(page.locator("form .lg-note a"));
    await expect(page.locator("#sign-in-password")).toHaveCount(1);
    await expect(page.getByRole("button", { name: "取消" })).toHaveCount(0);
    release();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  });

  test("a page the browser shows again from its back-forward cache loads afresh", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await expect(page.getByLabel("密码", { exact: true })).toBeVisible();
    // what the browser fires when it brings the page back from the cache, mid-request as the visitor left it
    await Promise.all([
      page.waitForEvent("load"),
      page.evaluate(() => window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }))),
    ]);
    const type = await page.evaluate(
      () => (performance.getEntriesByType("navigation")[0] as PerformanceNavigationTiming).type,
    );
    expect(type).toBe("reload");
    await expect(page.getByLabel("邮箱")).toHaveValue("");
  });

  test("switching the language keeps what was typed", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/sign-in");
    await page.getByLabel("邮箱").fill(knownEmail);
    await page.getByRole("button", { name: "继续", exact: true }).click();
    await page.getByLabel("密码", { exact: true }).fill(knownPassword);
    await page.getByRole("link", { name: "EN, English", exact: true }).click();
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
    // the whole address, inside its line's box: not clipped, nor run out under the form's edge
    const sub = page.locator(".lg-sub");
    await expect(sub).toContainText(`${"w".repeat(64)}@example.com`);
    expect(await sub.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
  });

  test("the language switch keeps its place and focus, and the current language does nothing", async ({ page }) => {
    await mockConsole(page);
    await page.goto("/console/sign-in");
    const chinese = page.getByRole("link", { name: "中文", exact: true });
    await expect(chinese).toHaveAttribute("aria-current", "true");
    await chinese.focus();
    await page.keyboard.press("Enter");
    expect(new URL(page.url()).hash).toBe("");
    const english = page.getByRole("link", { name: "EN, English", exact: true });
    await expect(english).toHaveAttribute("lang", "en");
    await english.focus();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("heading", { level: 1, name: "Sign in" })).toBeVisible();
    await expect(english).toHaveAttribute("aria-current", "true");
    await expect(english).toBeFocused();
    await expect(chinese).not.toHaveAttribute("aria-current", /.*/);
  });

  test("the site's brand colour is the focus ring of the sign-in fields", async ({ page }) => {
    const state = await mockConsole(page);
    const rings: string[] = [];
    for (const brand of ["#ff0000", "#335ddf"]) {
      state.site = { ...state.site, brand_color: brand };
      await page.goto("/console/sign-in");
      await page.getByLabel("邮箱").focus();
      const { ring, width, line, accent } = await page.evaluate(() => {
        const field = document.querySelector(".lg-field") as HTMLElement;
        const probe = document.createElement("i");
        probe.style.color = "var(--accent)";
        field.append(probe);
        const accent = getComputedStyle(probe).color;
        probe.remove();
        const style = getComputedStyle(field);
        return { ring: style.outlineColor, width: style.outlineWidth, line: style.outlineStyle, accent };
      });
      // a ring that is drawn, in the console's accent as brandStyle derives it from this site's colour
      expect(ring).toBe(accent);
      expect({ width, line }).toEqual({ width: "2px", line: "solid" });
      rings.push(ring);
    }
    expect(rings[0]).not.toBe(rings[1]);
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
    await expect.poll(async () => (await look()).background).not.toBe(disabled.fill);
    await submit.click();
    await expect(page.locator("form")).toHaveAttribute("aria-busy", "true");
    answer();
    await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  });

  test("a site name of 100 characters wraps beside the language switch, whole on a wide window", async ({ page }) => {
    const state = await mockConsole(page);
    state.site = { ...state.site, name: `${"W".repeat(60)} ${"W".repeat(39)}` };
    const brand = page.locator(".lg-sea .brand");
    const name = brand.locator("span").last();
    const cut = () =>
      name.evaluate(
        (element) => element.scrollWidth > element.clientWidth + 1 || element.scrollHeight > element.clientHeight + 1,
      );
    await page.goto("/console/sign-in");
    await expect(brand).toContainText("WWW");
    expect(await cut()).toBe(false);
    await page.setViewportSize({ width: 360, height: 800 });
    const nav = await page.locator(".lg-top").boundingBox();
    const box = await brand.boundingBox();
    const pitch = await page.locator(".lg-pitch").boundingBox();
    // clear of the language switch beside it and of the pitch under it
    expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(nav?.x ?? 0);
    expect((box?.y ?? 0) + (box?.height ?? 0)).toBeLessThanOrEqual(pitch?.y ?? 0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(360);
    // on a phone the name has its three lines, and only then ends
    const lines = await name.evaluate(
      (element) => element.clientHeight / Number.parseFloat(getComputedStyle(element).lineHeight),
    );
    expect(Math.round(lines)).toBe(3);
    expect(await cut()).toBe(true);
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
    await page.getByRole("link", { name: "EN, English", exact: true }).click();
    await expect(
      page.getByLabel("I have read and agree to the terms of service and privacy policy above."),
    ).toBeChecked();
    await expect(page.getByRole("button", { name: "Accept and continue" })).toBeEnabled();
  });
});

// A control's edge against its own fill, as the page computes them: WCAG 2.2's 3:1 (docs/frontend.md「对比度令牌」).
const rgb = (css: string) => (css.match(/\d+(\.\d+)?/g) ?? []).slice(0, 3).map(Number) as [number, number, number];

// The edge is an inset shadow: it must be drawn (a spread of at least a pixel) and opaque, or its colour says nothing.
async function edgeContrast(page: Page, selector: string) {
  const control = page.locator(selector).first();
  await expect(control).toBeVisible();
  const { edge, spread, fill } = await control.evaluate((element) => {
    const style = getComputedStyle(element);
    const edge = style.boxShadow.match(/rgba?\([^)]*\)/)?.[0] ?? "";
    const lengths = style.boxShadow.replace(edge, "").match(/-?[\d.]+px/g) ?? [];
    return { edge, spread: Number.parseFloat(lengths[3] ?? "0"), fill: style.backgroundColor };
  });
  expect(edge).toMatch(/^rgb\(/);
  expect(spread).toBeGreaterThanOrEqual(1);
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
