import "i18next";
import type { LoginMessages } from "@axiom/login";
import type { CommonMessages } from "@axiom/ui";
import type { initial as console } from "./locales/zh";

declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "console";
    resources: { common: CommonMessages; console: typeof console; login: LoginMessages };
  }
}
