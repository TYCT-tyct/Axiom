import type { Page } from "@playwright/test";
import { type MockData, mockConsole, signedInAs } from "./console-api.ts";
import { knownPassword } from "./console-data.ts";

// The screens of the console M1, each reached the way a user reaches it. The accessibility and layout checks and the
// screenshots of the acceptance gate (docs/frontend.md「验收门槛」) walk the same list, so a new screen is added once.

export type Scene = { name: string; signedIn: boolean; open: (page: Page) => Promise<void> };

const pageHeading = (page: Page) => page.getByRole("heading", { level: 1 });

async function goto(page: Page, path: string) {
  await page.goto(`/console/${path}`);
  await pageHeading(page).waitFor();
}

// A screen whose dialog opens after a click on the page.
const dialogScene = (name: string, path: string, open: (page: Page) => Promise<void>): Scene => ({
  name,
  signedIn: true,
  open: async (page) => {
    await goto(page, path);
    await open(page);
    await page.getByRole("dialog").waitFor();
  },
});

const scenes: Scene[] = [
  { name: "sign-in", signedIn: false, open: (page) => goto(page, "sign-in") },
  { name: "register", signedIn: false, open: (page) => goto(page, "register?invite=INVITE-OK") },
  { name: "overview", signedIn: true, open: (page) => goto(page, "") },
  { name: "balance", signedIn: true, open: (page) => goto(page, "balance") },
  { name: "keys", signedIn: true, open: (page) => goto(page, "keys") },
  { name: "calls", signedIn: true, open: (page) => goto(page, "calls") },
  { name: "call", signedIn: true, open: (page) => goto(page, "calls/00000000-0000-4000-8000-000000000001") },
  { name: "security", signedIn: true, open: (page) => goto(page, "security") },
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
export async function prepare(page: Page, scene: Scene, data: MockData = "usual") {
  const state = await mockConsole(page, data);
  if (scene.name === "mfa") {
    state.level = "pending_mfa";
    state.csrf = "csrf-0";
  } else if (scene.signedIn) {
    signedInAs(state);
  }
  return state;
}

export const allScenes = [...scenes, mfaScene];
