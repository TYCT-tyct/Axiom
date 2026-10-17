import "i18next";
import type { CommonMessages } from "@axiom/ui";

// Everything this package shows is in the shared namespace; each app declares its own besides.
declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "common";
    resources: { common: CommonMessages };
  }
}
