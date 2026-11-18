import type { Page } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";
import type { MockData } from "./mock.ts";
import { knownPassword } from "./mock-auth.ts";
import { pageHeading } from "./page-heading.ts";
import { type Scene, type Suite, dialogScene as scene } from "./scene.ts";

// The screens of the console M1.

async function goto(page: Page, path: string) {
  await page.goto(`/console/${path}`);
  await pageHeading(page);
}

// A screen whose dialog opens after a click on the page.
const dialogScene = (name: string, path: string, open: (page: Page) => Promise<void>): Scene =>
  scene(name, (page) => goto(page, path), open);

const scenes: Scene[] = [
  { name: "sign-in", signedIn: false, open: (page) => goto(page, "sign-in") },
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
      await page.locator("input[inputmode=decimal]").fill("10");
      await page.getByRole("button", { name: /^(查看支付方式|See how to pay)$/ }).click();
      await page.getByRole("combobox").waitFor();
    },
  },
  { name: "keys", signedIn: true, open: (page) => goto(page, "keys") },
  { name: "routing-presets", signedIn: true, open: (page) => goto(page, "routing-presets") },
  dialogScene("routing-preset-create", "routing-presets", (page) =>
    page.getByRole("button", { name: /^(新建预设|New preset)$/ }).click(),
  ),
  // A copy of a preset that asks for a product score, which the editor offers only to remove.
  dialogScene("routing-preset-copy", "routing-presets", (page) =>
    page
      .getByRole("button", { name: /^(复制|Copy) / })
      .first()
      .click(),
  ),
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
  // The account's first preset is its default and has keys bound (console-data.ts), so the dialog says both.
  dialogScene("routing-preset-disable", "routing-presets", (page) =>
    page
      .getByRole("button", { name: /^(停用|Disable) / })
      .first()
      .click(),
  ),
  {
    name: "routing-presets-tip",
    signedIn: true,
    open: async (page) => {
      await goto(page, "routing-presets");
      await page
        .getByRole("button", { name: /^(修改|Edit) / })
        .first()
        .hover();
    },
  },
  { name: "calls", signedIn: true, open: (page) => goto(page, "calls") },
  { name: "call", signedIn: true, open: (page) => goto(page, "calls/00000000-0000-4000-8000-000000000001") },
  { name: "security", signedIn: true, open: (page) => goto(page, "security") },
  { name: "settings", signedIn: true, open: (page) => goto(page, "settings") },
  dialogScene("key-create", "keys", (page) => page.getByRole("button", { name: /^(创建 key|Create key)$/ }).click()),
  dialogScene("key-edit", "keys", (page) =>
    page
      .getByRole("button", { name: /^(修改|Edit) / })
      .first()
      .click(),
  ),
  dialogScene("key-delete", "keys", (page) =>
    page
      .getByRole("button", { name: /^(删除|Delete) / })
      .first()
      .click(),
  ),
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

// prepare mocks the API for a scene on a page: with the data the scene is shown with, and the session it needs.
async function prepare(page: Page, scene: Scene, data: MockData = "usual") {
  const state = await mockConsole(page, data);
  if (scene.name === "mfa") {
    state.level = "pending_mfa";
    state.csrf = "csrf-0";
  } else if (scene.signedIn) {
    signedInAs(state);
  }
  return state;
}

export const consoleSuite: Suite = {
  app: "console",
  scenes: [...scenes, mfaScene],
  prepare,
  brands: ["sign-in", "overview", "keys", "key-create"],
};
