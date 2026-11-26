import type { Page, Route } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { uuid } from "./admin-data.ts";
import { adminUrl } from "./apps.ts";
import type { MockData } from "./mock.ts";
import { signedInAs } from "./mock-auth.ts";
import { pageHeading } from "./page-heading.ts";
import { type Scene, type Suite, dialogScene as scene } from "./scene.ts";
import { words } from "./words.ts";

// The screens of the admin M1, each reached the way an operator reaches it.

export const acct = (n: number) => `acct_${String(n).padStart(26, "0")}`;

async function goto(page: Page, path: string) {
  await page.goto(adminUrl(path));
  await pageHeading(page);
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

// A list whose first row is opened into its drawer.
const rowDrawer = (name: string, path: string): Scene =>
  scene(
    name,
    (p) => goto(p, path),
    (p) => p.locator("tbody tr").first().locator("td").first().getByRole("button").click(),
  );

// The routing presets page with the platform's switch in a given state whatever the data set says (the worst set has it
// on), so the suspended page and each of its two dialogs are in every width, scheme and language. The dialogs that
// change presets open with it off: while it is on their buttons are disabled.
async function routingPage(p: Page, suspended: boolean, tab = "") {
  await p.route("**/api/admin/v1/settings/routing-suspension", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({ json: suspended ? { suspended, since: "2026-11-01T08:00:00Z" } : { suspended } })
      : route.fallback(),
  );
  await goto(p, `routing${tab}`);
}

const routingSuspended: Scene = { name: "routing-suspended", signedIn: true, open: (p) => routingPage(p, true) };
const routingSwitch = (name: string, suspended: boolean, tab: string, ...buttons: string[]) =>
  scene(
    name,
    (p) => routingPage(p, suspended, tab),
    async (p) => {
      for (const button of buttons) {
        await press(p, button);
      }
    },
  );

// The second preset's drawer (the first with a name that changes), and the buttons pressed in it.
const routingPreset = (name: string, ...buttons: string[]) =>
  scene(
    name,
    (p) => routingPage(p, false),
    async (p) => {
      await p
        .getByRole("button", { name: words("routingPresets.open") })
        .nth(1)
        .click();
      for (const button of buttons) {
        await press(p, button);
      }
    },
  );

const audienceScenes = (["tier", "account"] as const).map((kind) =>
  scene(
    `rule-${kind}`,
    (p) => goto(p, "pricing?tab=rules"),
    async (p) => {
      await press(p, "pricing.rule.new");
      await p.getByRole("dialog").locator("select[name=audience_kind]").selectOption(kind);
    },
  ),
);

const pricingPreview: Scene = {
  name: "pricing-preview",
  signedIn: true,
  open: async (p) => {
    await goto(p, "pricing?tab=rules");
    await press(p, "pricing.preview.run");
    await p.getByRole("table", { name: words("pricing.preview.title") }).waitFor();
  },
};

const supplier = (n: number, tab?: string) => `suppliers/${uuid(2, n)}${tab ? `?tab=${tab}` : ""}`;

// A channel's endpoints are its pile of models on sale, folded on the models tab until opened.
const openListed = (p: Page) => p.getByText(words("upstream.models.listed"), { exact: true }).click();

// A dialog opened from a channel's endpoints, after the pile that holds them is opened.
const endpointDialog = (name: string, n: number, button: string) =>
  scene(
    name,
    (p) => goto(p, supplier(n, "models")),
    async (p) => {
      await openListed(p);
      await press(p, button);
    },
  );
const model = (n: number) => `models?model=${uuid(5, n)}`;

// The connect dialog's second step: an address the probe recognises.
const connectDetails = scene(
  "supplier-connect-details",
  (p) => goto(p, "suppliers"),
  async (p) => {
    await press(p, "upstream.add");
    await p.getByRole("dialog").locator("input[name=origin]").fill("https://api.example.cn");
    await p.getByRole("dialog").locator("button[type=submit]").click();
    await p.getByRole("dialog").locator("input[name=upstream_account]").waitFor();
  },
);

// An endpoint test that has answered, for the first endpoint: with the usual data the mock fails it (a cause to show),
// with the worst it passes it under a 200-character upstream name (admin-data.ts, admin-supply.ts).
const testResult = scene(
  "supplier-test-result",
  (p) => goto(p, supplier(1, "models")),
  async (p) => {
    await openListed(p);
    await press(p, "suppliers.test.title");
    const form = p.getByRole("dialog");
    await form.getByLabel(words("change.reason")).fill("check the endpoint after the key rotation");
    await form.locator("button[type=submit]").click();
    await form.getByText(words("suppliers.test.total")).waitFor();
  },
);

// A change dialog after an answer that did not settle the change: the server's answer to the first send is replaced, so
// the dialog shows what it does next (change.tsx).
const unsettled = (name: string, path: string, open: string, fill: (p: Page) => Promise<unknown>, answer: Answer) =>
  scene(
    name,
    (p) => goto(p, path),
    async (p) => {
      await press(p, open);
      const form = p.getByRole("dialog");
      await fill(p);
      await form.getByLabel(words("change.reason")).fill("because the owner asked");
      await p.route(`**${answer.path}`, (route) => answer.reply(route), { times: 1 });
      await form.locator("button[type=submit]").click();
      await form
        .getByRole("alert")
        .getByText(words(`change.${answer.state}`))
        .waitFor();
    },
  );

type Answer = { path: string; state: string; reply: (route: Route) => Promise<void> };
const refused = (code: string) => (route: Route) =>
  route.fulfill({ status: 409, json: { error: { code, message: code } } });
const amount = (p: Page) => p.getByRole("dialog").getByLabel(words("users.credit.amount")).fill("2.5");
const grant = (state: string, reply: Answer["reply"]) => ({ path: "/credit-grants", state, reply });

const rateLimit: Scene = {
  name: "rate-limits",
  signedIn: true,
  open: async (p) => {
    await goto(p, "rate-limits");
    await p.getByLabel(words("rateLimits.target")).selectOption("account");
    await p.getByLabel(words("rateLimits.id")).fill(acct(1));
    await press(p, "rateLimits.lookup");
    await p.getByRole("heading", { name: words("rateLimits.allowance") }).waitFor();
  },
};

const scenes: Scene[] = [
  view("sign-in", "sign-in", false),
  view("home", ""),
  view("users", "users"),
  view("orders", "orders"),
  view("account", `accounts/${acct(1)}`),
  view("calls", "calls"),
  rowDrawer("call", "calls"),
  view("audit", "audit"),
  rowDrawer("audit-entry", "audit"),
  view("plugins", "plugins"),
  view("plugins-web", "plugins/web"),
  view("plugins-file", "plugins/file"),
  view("me", "me"),
  view("suppliers", "suppliers"),
  view("supplier", supplier(1)),
  view("supplier-setup", supplier(2)),
  view("supplier-attention", supplier(3)),
  view("upstream-models", "suppliers?tab=models"),
  view("upstream-inbox", "suppliers?tab=inbox"),
  view("upstream-keys", "suppliers?tab=keys"),
  view("supplier-models", supplier(1, "models")),
  view("supplier-keys", supplier(1, "keys")),
  view("supplier-sync", supplier(1, "sync")),
  view("supplier-settings", supplier(1, "settings")),
  view("models", "models"),
  view("model", model(1)),
  view("model-draft", model(3)),
  view("pricing", "pricing"),
  view("pricing-rules", "pricing?tab=rules"),
  view("pricing-policy", "pricing?tab=policy"),
  dialog("price-policy", "pricing?tab=policy", "pricePolicy.edit"),
  view("exchange-rates", "pricing?tab=rates"),
  dialog("exchange-rate-history", "pricing?tab=rates", "exchangeRates.history"),
  view("payment-discrepancies", "payment-discrepancies"),
  view("payment-discrepancy", `payment-discrepancies/${uuid(33, 1)}`),
  dialog("platform-grant", `accounts/${acct(1)}`, "platformGrants.revoke"),
  pricingPreview,
  ...audienceScenes,
  view("subscription-plans", "subscription-plans"),
  dialog("subscription-plan-new", "subscription-plans", "subscriptions.new"),
  dialog("subscription-plan-edit", "subscription-plans", "subscriptions.editNamed"),
  view("settings", "settings/site"),
  view("settings-sign-in", "settings/sign-in"),
  view("settings-registration", "settings/registration"),
  view("settings-sessions", "settings/sessions"),
  view("settings-limits", "settings/limits"),
  view("settings-payments", "settings/payments"),
  view("settings-mail", "settings/mail"),
  view("settings-gateway", "settings/gateway"),
  view("monitor", "monitor"),
  view("alert-events", "alert-events"),
  view("alert-configuration", "alert-configuration"),
  view("announcements", "announcements"),
  view("legal-documents", "legal-documents"),
  rateLimit,
  view("routing", "routing"),
  view("routing-source-classes", "routing?tab=sources"),
  routingSwitch("routing-preset-create", false, "", "routingPresets.createTitle.platform"),
  // The first preset with a name to open is the system's; the second is a default with keys bound (admin-routing.ts), so
  // its drawer says the order and its disable dialog says both.
  routingPreset("routing-preset"),
  routingPreset("routing-preset-edit", "routingPresets.order.rules"),
  routingSwitch("routing-default", false, "?tab=defaults", "routingPresets.defaults.edit"),
  routingPreset("routing-preset-disable", "routingPresets.disable"),
  dialog("routing-limits", "routing?tab=defaults", "routingPresets.limits.edit"),
  routingSuspended,
  routingSwitch("routing-suspend", false, "?tab=defaults", "routingPresets.suspension.suspend"),
  routingSwitch("routing-resume", true, "?tab=defaults", "routingPresets.suspension.resume"),
  dialog("credit", `accounts/${acct(1)}`, "users.credit.open"),
  view("approvals", "approvals"),
  // The first approval is a credit grant with its caps, the third a sale price with its envelope (admin-operations.ts).
  view("approval", `approvals/${uuid(17, 1)}`),
  view("approval-price", `approvals/${uuid(17, 3)}`),
  dialog("approval-approve", `approvals/${uuid(17, 1)}`, "approvals.approve"),
  dialog("approval-reject", `approvals/${uuid(17, 1)}`, "approvals.reject"),
  view("settings-agent", "settings/agent"),
  dialog("operations-credential-new", "settings/agent", "operations.credentials.new"),
  dialog("operations-credential-rotate", "settings/agent", "operations.credentials.rotateNamed"),
  dialog("operations-credential-revoke", "settings/agent", "operations.credentials.revokeNamed"),
  dialog("operations-cap-edit", "settings/agent", "operations.limits.cap.editNamed"),
  dialog("operations-agent-stop", "settings/agent", "operations.limits.stop.open"),
  dialog("refund-approve", "orders", "orders.refund.approve"),
  dialog("refund-reject", "orders", "orders.refund.reject"),
  dialog("account-suspend", `accounts/${acct(1)}`, "users.account.suspend"),
  dialog("account-sign-in", `accounts/${acct(1)}`, "users.account.disableSignIn"),
  dialog("account-tier", `accounts/${acct(1)}`, "users.account.changeTier"),
  dialog("account-subscription-grant", `accounts/${acct(1)}`, "subscriptions.account.grant"),
  dialog("account-subscription-refund", `accounts/${acct(1)}`, "subscriptions.account.refundRequest"),
  dialog("account-subscription-refund-approve", `accounts/${acct(1)}`, "subscriptions.account.refundApprove"),
  dialog("account-workspace-limit", `accounts/${acct(1)}`, "settings.workspaceLimit.edit"),
  dialog("supplier-connect", "suppliers", "upstream.add"),
  connectDetails,
  dialog("supplier-unit", supplier(2, "settings"), "suppliers.unit.open"),
  dialog("supplier-login", supplier(1, "settings"), "suppliers.login.open"),
  dialog("supplier-remove", supplier(4, "settings"), "suppliers.remove.open"),
  dialog("supplier-pause", supplier(2), "upstream.detail.pause"),
  dialog("supplier-bind", supplier(1, "keys"), "suppliers.bind.action"),
  dialog("supplier-test-all", supplier(1, "keys"), "upstream.keys.testAll"),
  endpointDialog("supplier-endpoint-disable", 1, "suppliers.endpointEnabled.disable.title"),
  endpointDialog("supplier-test", 1, "suppliers.test.title"),
  testResult,
  dialog("mapping", "suppliers?tab=inbox", "upstream.inbox.item.unknown_model.act"),
  dialog("model-draft-new", "models", "models.draft.open"),
  dialog("author", "models?tab=authors", "models.authors.add"),
  dialog("model-spec", model(1), "models.spec.edit"),
  dialog("model-name", model(1), "models.names.add"),
  dialog("model-list", model(3), "models.status.listed.action"),
  dialog("model-retire", model(1), "models.status.retired.action"),
  dialog("price-publish", model(1), "models.prices.publish"),
  dialog("price-source", model(1), "models.prices.changeSource"),
  dialog("rule-new", "pricing?tab=rules", "pricing.rule.new"),
  dialog("targets", "", "overview.targets.edit"),
  unsettled(
    "change-unknown",
    `accounts/${acct(1)}`,
    "users.credit.open",
    amount,
    grant("unknown", (r) => r.abort()),
  ),
  unsettled(
    "change-reused",
    `accounts/${acct(1)}`,
    "users.credit.open",
    amount,
    grant("reused", refused("idempotency_key_reused")),
  ),
  unsettled("change-stale", model(1), "models.spec.edit", async () => {}, {
    path: "/spec",
    state: "stale",
    reply: refused("stale_revision"),
  }),
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
