import type { AdminSchemas } from "@axiom/api";
import { createInstance } from "i18next";
import { renderToStaticMarkup } from "react-dom/server";
import { I18nextProvider } from "react-i18next";
import { beforeAll, expect, it } from "vitest";
import pricing from "../locales/zh/pricing";
import { MethodSummary } from "./describe";

const i18n = createInstance();

beforeAll(async () => {
  await i18n.init({ lng: "zh", defaultNS: "admin", resources: { zh: { admin: { pricing } } } });
});

it.each<[AdminSchemas["PricingMethod"], string]>([
  [{ kind: "cost_plus", markup_ratio: "0.2" }, "成本加价 20%"],
  [{ kind: "reference_multiplier", multiplier: "1.5" }, "参考价的 1.5 倍"],
  [{ kind: "default_multiplier", multiplier: "1.1" }, "全部账户售价 × 1.1"],
  [{ kind: "fixed" }, "固定价（—）"],
])("describes %j as %s", (method, text) => {
  const html = renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <MethodSummary method={method} />
    </I18nextProvider>,
  );
  expect(html).toBe(text);
});
