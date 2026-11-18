import type { Page } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";
import { demoDocuments, goodResetToken, type Schemas } from "./console-data.ts";
import type { MockData } from "./mock.ts";
import { knownPassword } from "./mock-auth.ts";
import { pageHeading } from "./page-heading.ts";
import { type Scene, type Suite, dialogScene as scene } from "./scene.ts";

// User content is independent of the UI language. Use the same prompts in the API and the reference.
export const requestPresetPrompts = [
  "Keep the meaning and shorten the text.",
  "Outline the topic in three sections.",
] as const;

// The screens of the console M1.

async function goto(page: Page, path: string) {
  await page.goto(`/console/${path}`);
  await pageHeading(page);
}

// A row of the routing page's table: its name is the button that opens the preset, which the board's table draws. The
// row of the preset a key made now takes is the one the page marks, whichever data and language it shows.
async function openPreset(page: Page, which: "first" | "default") {
  const row = which === "default" ? page.locator("tbody tr[data-default=true]") : page.locator("tbody tr").first();
  await row.locator("b[role=button]").click();
}

// A screen whose dialog opens after a click on the page.
const dialogScene = (name: string, path: string, open: (page: Page) => Promise<void>): Scene =>
  scene(name, (page) => goto(page, path), open);

const scenes: Scene[] = [
  { name: "sign-in", signedIn: false, open: (page) => goto(page, "sign-in") },
  {
    // the sign-in page's second step, the password, after the email
    name: "sign-in-password",
    signedIn: false,
    open: async (page) => {
      await goto(page, "sign-in");
      await page.getByLabel(/^(邮箱|Email)$/).fill("you@example.com");
      await page.getByRole("button", { name: /^(继续|Continue)$/ }).click();
      await page.getByLabel(/^(密码|Password)$/).waitFor();
    },
  },
  // the reset email's link: the new password, its token in the fragment
  { name: "reset-password", signedIn: false, open: (page) => goto(page, `reset-password#token=${goodResetToken}`) },
  // a member whose accepted terms are behind the site's current ones
  { name: "consent", signedIn: true, open: (page) => goto(page, "consent") },
  { name: "register", signedIn: false, open: (page) => goto(page, "register?invite=INVITE-OK") },
  { name: "overview", signedIn: true, open: (page) => goto(page, "") },
  { name: "balance", signedIn: true, open: (page) => goto(page, "balance") },
  { name: "statement", signedIn: true, open: (page) => goto(page, "statement") },
  { name: "orders", signedIn: true, open: (page) => goto(page, "orders") },
  { name: "models", signedIn: true, open: (page) => goto(page, "models") },
  { name: "notifications", signedIn: true, open: (page) => goto(page, "notifications") },
  {
    name: "top-up",
    signedIn: true,
    open: async (page) => {
      await goto(page, "balance");
      await page
        .getByRole("button", { name: /^(充值|Top up)$/ })
        .first()
        .click();
      const amount = page.locator("input[inputmode=decimal]");
      await amount.waitFor();
      await amount.fill("10");
      await page.locator("button[data-m]").first().waitFor();
    },
  },
  { name: "keys", signedIn: true, open: (page) => goto(page, "keys") },
  { name: "request-presets", signedIn: true, open: (page) => goto(page, "request-presets") },
  { name: "routing-presets", signedIn: true, open: (page) => goto(page, "routing-presets") },
  dialogScene("routing-preset-create", "routing-presets", (page) =>
    page.getByRole("button", { name: /^(新建预设|New preset)$/ }).click(),
  ),
  // The row's name opens the preset, which is where its ways on live: a copy that asks for a product score, which the
  // editor offers only to remove.
  dialogScene("routing-preset-copy", "routing-presets", async (page) => {
    await openPreset(page, "first");
    await page
      .getByRole("dialog")
      .getByRole("button", { name: /^(复制|Copy)$/ })
      .click();
  }),
  // A rule whose data policies keep no endpoint, as the check says, refused on save.
  dialogScene("routing-preset-refused", "routing-presets", async (page) => {
    await page.getByRole("button", { name: /^(新建预设|New preset)$/ }).click();
    const dialog = page.getByRole("dialog");
    await dialog
      .getByLabel(/^(限定数据政策|Limit the data policies)$/)
      .first()
      .check();
    await dialog
      .getByLabel(/^(可能用于训练|May train on data)$/)
      .first()
      .uncheck();
    await dialog.locator("button[type=submit]").click();
    await dialog.locator("[data-invalid]").waitFor();
  }),
  // The preset a key made now takes is the account's own, which has keys bound (console-data.ts), so the dialog says
  // both.
  dialogScene("routing-preset-disable", "routing-presets", async (page) => {
    await openPreset(page, "default");
    await page
      .getByRole("dialog")
      .getByRole("button", { name: /^(停用|Disable)$/ })
      .click();
  }),
  // The preset's own dialog: its rules in a line, the account's default, and the ways on.
  dialogScene("routing-preset-detail", "routing-presets", (page) => openPreset(page, "default")),
  // The board's BYOK page: the account's own provider keys, each row opening its test, switch and removal.
  { name: "provider-keys", signedIn: true, open: (page) => goto(page, "provider-keys") },
  dialogScene("provider-key-detail", "provider-keys", (page) =>
    page
      .getByRole("button", { name: /^(打开|Open) / })
      .first()
      .click(),
  ),
  { name: "usage", signedIn: true, open: (page) => goto(page, "usage") },
  { name: "workspaces", signedIn: true, open: (page) => goto(page, "workspaces") },
  { name: "calls", signedIn: true, open: (page) => goto(page, "calls") },
  { name: "call", signedIn: true, open: (page) => goto(page, "calls/00000000-0000-4000-8000-000000000001") },
  { name: "security", signedIn: true, open: (page) => goto(page, "security") },
  { name: "settings", signedIn: true, open: (page) => goto(page, "settings") },
  dialogScene("key-create", "keys", (page) => page.getByRole("button", { name: /^(创建 key|Create key)$/ }).click()),
  // A key opens from its row in the list, as the board draws it; its settings, and the way to delete it, are there. The
  // first row is a key in every data set the scenes run with, from the usual account to the worst one.
  dialogScene("key-edit", "keys", (page) => page.locator(".p-keys tbody tr").first().click()),
  dialogScene("key-delete", "keys", async (page) => {
    await page.locator(".p-keys tbody tr").first().click();
    await page
      .getByRole("dialog", { name: /^(修改 key|Edit key)$/ })
      .getByRole("button", { name: /^(删除|Delete) / })
      .click();
  }),
  dialogScene("key-secret", "keys", async (page) => {
    await page.getByRole("button", { name: /^(创建 key|Create key)$/ }).click();
    await page.getByRole("dialog").locator("input[name=name]").fill("Deploy bot");
    await page.getByRole("dialog").locator("button[type=submit]").click();
  }),
  {
    name: "model-details",
    signedIn: true,
    open: async (page) => {
      await goto(page, "models");
      await page
        .getByRole("button", { name: /^(详情|Details)$/ })
        .first()
        .click();
      await page.locator("#main [aria-expanded=true]").waitFor();
    },
  },
  dialogScene("refund", "orders", (page) =>
    page
      .getByRole("button", { name: /^(申请退款|Request refund)$/ })
      .first()
      .click(),
  ),
  dialogScene("totp-setup", "security", (page) =>
    page.getByRole("button", { name: /^(开启两步验证|Turn on two-step verification)$/ }).click(),
  ),
  dialogScene("recovery-codes", "security", async (page) => {
    await page.getByRole("button", { name: /^(开启两步验证|Turn on two-step verification)$/ }).click();
    const dialog = page.getByRole("dialog");
    await dialog.locator("input[name=password]").fill(knownPassword);
    await dialog.locator("input[name=code]").fill("123456");
    await dialog.locator("button[type=submit]").click();
    await page.getByRole("listitem").first().waitFor();
  }),
];

// The pending second step is signed-out in the API's eyes but needs its own state.
const mfaScene: Scene = {
  name: "mfa",
  signedIn: false,
  open: (page) => goto(page, "mfa"),
};

function spreadUsageCalls(calls: Schemas["Call"][], endUtcMs: number): Schemas["Call"][] {
  const dayMs = 86_400_000;
  const endDay = Math.floor(endUtcMs / dayMs);
  const anchor = endDay * dayMs + 8 * 3_600_000;
  const out: Schemas["Call"][] = [];
  calls.forEach((call, i) => {
    const dayOffset = Math.floor(i / 4) % 24;
    out.push({ ...call, at: new Date(anchor - dayOffset * dayMs - (i % 7) * 60_000).toISOString() });
    if (i % 3 !== 0) {
      out.push({ ...call, at: new Date(anchor - (25 + dayOffset) * dayMs - (i % 5) * 60_000).toISOString() });
    }
  });
  return out;
}

// prepare mocks the API for a scene on a page: with the data the scene is shown with, and the session it needs.
async function prepare(page: Page, scene: Scene, data: MockData = "usual") {
  const state = await mockConsole(page, data);
  if (scene.name === "usage" && data !== "empty") {
    const demoAnchor = Date.UTC(2026, 9, 24, 2, 0, 0);
    state.calls = [...spreadUsageCalls(state.calls, demoAnchor), ...spreadUsageCalls(state.calls, Date.now())];
  }
  if (scene.name === "consent") {
    state.legal = demoDocuments();
  }
  if (scene.name === "mfa") {
    state.level = "pending_mfa";
    state.csrf = "csrf-0";
  } else if (scene.signedIn) {
    signedInAs(state);
  }
  if (scene.name === "request-presets") {
    await page.route("**/api/console/v1/presets?*", (route) => {
      const presets = [
        {
          slug: "polish-zh",
          name: "polish-zh",
          config: {
            model: "claude-sonnet-5.5",
            system: requestPresetPrompts[0],
            temperature: 0.4,
            max_tokens: 2000,
          },
        },
        {
          slug: "outline",
          name: "outline",
          config: {
            model: "gpt-6.1-sol",
            system: requestPresetPrompts[1],
            temperature: 0.6,
            max_tokens: 600,
          },
        },
      ];
      return route.fulfill({ json: { data: data === "empty" ? [] : presets, total_count: data === "empty" ? 0 : 2 } });
    });
  }
  return state;
}

export const consoleSuite: Suite = {
  app: "console",
  scenes: [...scenes, mfaScene],
  prepare,
  brands: ["sign-in", "overview", "keys", "key-create"],
};
