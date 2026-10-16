import type { Page } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { uuid } from "./admin-data.ts";
import { adminUrl } from "./apps.ts";
import type { MockData } from "./mock.ts";
import { signedInAs } from "./mock-auth.ts";
import { type Scene, type Suite, dialogScene as scene } from "./scene.ts";
import { words } from "./words.ts";

// The screens of the admin M1, each reached the way an operator reaches it.

export const acct = (n: number) => `acct_${String(n).padStart(26, "0")}`;

async function goto(page: Page, path: string) {
  await page.goto(adminUrl(path));
  await page.getByRole("heading", { level: 1 }).waitFor();
}

const press = (page: Page, name: string, nth = 0) =>
  page
    .getByRole("button", { name: words(name) })
    .nth(nth)
    .click();

const view = (name: string, path: string, signedIn = true): Scene => ({
  name,
  signedIn,
  open: (p) => goto(p, path),
});

// A screen whose dialog opens after buttons are pressed on the page.
const dialog = (name: string, path: string, ...buttons: string[]): Scene =>
  scene(
    name,
    (p) => goto(p, path),
    async (p) => {
      for (const button of buttons) {
        await press(p, button);
      }
    },
  );

const supplier = (n: number) => `suppliers/${uuid(2, n)}`;
const model = (n: number) => `models/${uuid(5, n)}`;

// The connect dialog's second step: an address the probe recognises.
const connectDetails = scene(
  "supplier-connect-details",
  (p) => goto(p, "suppliers"),
  async (p) => {
    await press(p, "suppliers.connect.open");
    await p.getByRole("dialog").locator("input[name=origin]").fill("https://api.example.cn");
    await p.getByRole("dialog").locator("button[type=submit]").click();
    await p.getByRole("dialog").locator("input[name=upstream_account]").waitFor();
  },
);

const scenes: Scene[] = [
  view("sign-in", "sign-in", false),
  view("users", "users"),
  view("account", `accounts/${acct(1)}`),
  view("suppliers", "suppliers"),
  view("supplier", supplier(1)),
  view("supplier-setup", supplier(2)),
  view("supplier-attention", supplier(3)),
  view("mappings", "mappings"),
  view("models", "models"),
  view("model", model(1)),
  view("model-draft", model(3)),
  view("pricing", "pricing"),
  view("settings", "settings"),
  dialog("credit", `accounts/${acct(1)}`, "users.credit.open"),
  dialog("supplier-connect", "suppliers", "suppliers.connect.open"),
  connectDetails,
  dialog("supplier-unit", supplier(2), "suppliers.unit.open"),
  dialog("supplier-login", supplier(1), "suppliers.login.open"),
  dialog("supplier-remove", supplier(4), "suppliers.remove.open"),
  dialog("supplier-pause", supplier(2), "suppliers.status.paused.action"),
  dialog("supplier-group", supplier(1), "suppliers.group.unselect.action"),
  dialog("supplier-bind", supplier(1), "suppliers.bind.action"),
  dialog("mapping", "mappings", "mappings.map"),
  dialog("model-draft-new", "models", "models.draft.open"),
  dialog("author", "models", "models.authors.add"),
  dialog("model-spec", model(1), "models.spec.edit"),
  dialog("model-name", model(1), "models.names.add"),
  dialog("model-list", model(3), "models.status.listed.action"),
  dialog("model-retire", model(1), "models.status.retired.action"),
  dialog("price-publish", model(1), "models.prices.publish"),
  dialog("price-source", model(1), "models.prices.changeSource"),
  dialog("rule-new", "pricing", "pricing.rule.new"),
  dialog("settings-edit", "settings", "settings.edit"),
];

// A pending second step and a password that still needs a factor are signed out in the API's eyes, each in its own state.
const pending: Scene = { name: "mfa", signedIn: false, open: (p) => goto(p, "mfa") };
const enroll: Scene = { name: "enroll", signedIn: false, open: (p) => goto(p, "enroll") };

// prepare mocks the API for a scene on a page: with the data the scene is shown with, and the session it needs.
async function prepare(p: Page, sceneToShow: Scene, data: MockData = "usual") {
  const state = await mockAdmin(p, data);
  if (sceneToShow.name === "mfa") {
    state.level = "pending_mfa";
    state.csrf = "csrf-0";
  } else if (sceneToShow.name === "enroll") {
    signedInAs(state, "password");
  } else if (sceneToShow.signedIn) {
    signedInAs(state, "mfa");
  }
  return state;
}

export const adminSuite: Suite = {
  app: "admin",
  scenes: [...scenes, pending, enroll],
  prepare,
  brands: ["sign-in", "users", "suppliers", "supplier-connect"],
};
