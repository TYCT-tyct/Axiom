import type { AdminSchemas } from "@axiom/api";
import { createInstance } from "i18next";
import { renderToStaticMarkup } from "react-dom/server";
import { I18nextProvider } from "react-i18next";
import { beforeAll, expect, it } from "vitest";
import { MerchantFields } from "./merchant-form";

const i18n = createInstance();

beforeAll(async () => {
  await i18n.init({ lng: "en", defaultNS: "admin", resources: { admin: { translation: {} } } });
});

it("starts a new WeChat Pay merchant with wxpay mapped to native", () => {
  const merchant: AdminSchemas["Merchant"] = {
    kind: "wechatpay",
    name: "",
    enabled: true,
    refund_enabled: false,
    user_refund: false,
    ttl_seconds: 1800,
    currency: "CNY",
    sort: 0,
    settings: { app_id: "", mch_id: "", serial_no: "", gateway: "https://api.mch.weixin.qq.com" },
    methods: [],
  };
  const html = renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <MerchantFields merchant={merchant} currency="CNY" error={undefined} />
    </I18nextProvider>,
  );
  expect(html).toContain('name="method-0-method"');
  expect(html).toContain('value="wxpay"');
  expect(html).toContain('name="method-0-upstream"');
  expect(html).toContain('value="native"');
});
