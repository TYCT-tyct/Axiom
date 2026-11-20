import { createInstance } from "i18next";
import { renderToStaticMarkup } from "react-dom/server";
import { I18nextProvider } from "react-i18next";
import { beforeAll, expect, it } from "vitest";
import en from "../locales/en";
import { ModelCatalog, type PublicEndpoint, type PublicModel } from "./models";

const i18n = createInstance();

beforeAll(async () => {
  await i18n.init({ lng: "en", defaultNS: "common", resources: { en: { common: en } } });
});

const endpoint: PublicEndpoint = {
  name: "group-a",
  context_length: 1_000,
  max_output: 100,
  streams: true,
  abilities: ["tools"],
  verified: true,
};

const model: PublicModel = {
  id: "acme/model",
  name: "Model",
  author: "acme",
  context_length: 1_000,
  max_output: 100,
  streams: true,
  abilities: ["tools"],
  endpoints: [endpoint],
};

const price = {
  currency: "USD",
  rates: [
    {
      service_tier: "standard",
      item: "input",
      unit: "token",
      amount: "0.000001",
      headline: true,
      display_amount: "1",
      display_per: 1_000_000,
    },
  ],
};

const render = (models: PublicModel[]) =>
  renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <ModelCatalog models={models} />
    </I18nextProvider>,
  );

// A site that keeps its prices to signed-in members leaves every pricing out of the anonymous projection. The page must
// read that as a no-price state: the models are still listed, no row claims a price is unavailable, and the note says
// who can see them (docs/domains/gateway.md「模型与价格页」).
it("lists models without prices when the site keeps them to members", () => {
  const html = render([model]);
  expect(html).toContain("Prices are visible to signed-in members");
  expect(html).not.toContain("Prices unavailable");
  expect(html).not.toContain("$");
  expect(html).toContain("Model");
});

// The public state is unchanged: the model's and its endpoint's prices are both shown, and the no-price note is not.
it("shows the model and endpoint prices when the site publishes them", () => {
  const html = render([
    {
      ...model,
      pricing: price,
      endpoints: [{ ...endpoint, pricing: price }],
    },
  ]);
  expect(html).toContain("$1.00");
  expect(html).not.toContain("Prices are visible to signed-in members");
});

// The list shows the rate the server names as the headline and the amount it says to show, not a rate the page picks:
// here the server names a priority rate, which the page must not replace with the standard one.
it("shows the headline rate and display amount the server names", () => {
  const named = {
    currency: "USD",
    rates: [
      {
        service_tier: "standard",
        item: "output",
        unit: "token",
        amount: "0.000003",
        headline: false,
        display_amount: "3",
        display_per: 1_000_000,
      },
      {
        service_tier: "priority",
        item: "output",
        unit: "token",
        amount: "0.000007",
        headline: true,
        display_amount: "7",
        display_per: 1_000_000,
      },
    ],
  };
  const html = render([{ ...model, pricing: named, endpoints: [] }]);
  expect(html).toMatch(/\$7\.00<\/span><div[^>]*>per 1M tokens/);
  expect(html).not.toMatch(/\$3\.00<\/span><div[^>]*>per 1M tokens/);
});

// An older node names no headline and no display amount: the list says the price is unavailable rather than choosing
// one, and the full rate table shows each price per unit exactly as it came.
it("shows an older node's prices per unit and no headline", () => {
  const html = render([
    {
      ...model,
      pricing: {
        currency: "USD",
        rates: [{ service_tier: "standard", item: "input", unit: "token", amount: "0.000001" }],
      },
      endpoints: [],
    },
  ]);
  expect(html).toContain("Prices unavailable");
  expect(html).not.toContain("$1.00");
});
