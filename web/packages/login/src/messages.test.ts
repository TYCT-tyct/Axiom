import { createInstance } from "i18next";
import { describe, expect, it } from "vitest";
import { registerLoginLinkFailed, registerLoginMessages } from "./index";

describe("registerLoginMessages", () => {
  it("registers zh and en bundles under the login namespace idempotently", async () => {
    const i18n = createInstance();
    await i18n.init({ lng: "zh", fallbackLng: false, resources: {} });

    registerLoginMessages(i18n);
    registerLoginMessages(i18n);

    expect(i18n.hasResourceBundle("zh", "login")).toBe(true);
    expect(i18n.hasResourceBundle("en", "login")).toBe(true);
    expect(i18n.t("auth.linkFailed", { ns: "login" })).toContain("回到邮件");
    await i18n.changeLanguage("en");
    expect(i18n.t("legal.terms", { ns: "login" })).toBe("Terms of Service");
  });

  it("still registers the full bundle after registerLoginLinkFailed ran first", async () => {
    const i18n = createInstance();
    await i18n.init({ lng: "zh", fallbackLng: false, resources: {} });

    registerLoginLinkFailed(i18n);
    expect(i18n.t("auth.linkFailed", { ns: "login" })).toContain("回到邮件");

    registerLoginMessages(i18n);
    expect(i18n.t("legal.terms", { ns: "login" })).toBe("服务条款");
    await i18n.changeLanguage("en");
    expect(i18n.t("auth.signIn.next", { ns: "login" })).toBe("Continue");
  });
});
