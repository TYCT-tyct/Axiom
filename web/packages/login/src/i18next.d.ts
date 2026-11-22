import "i18next";
import type { CommonMessages } from "@axiom/ui";
import type { LoginMessages } from "./locales";

// Message keys are checked when this package is type-checked on its own; each app declares its own namespaces.
declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "login";
    resources: { common: CommonMessages; login: LoginMessages };
  }
}
