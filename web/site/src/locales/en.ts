import type { Messages } from "@axiom/ui";
import type zh from "./zh";

export default {
  home: "Home",
  navigation: "Main navigation",
  models: "Models & prices",
  signIn: "Sign in",
  hero: {
    title: "One key for every major model",
    body: "Every route is measured, a failing one is swapped before the first token, and you pay for the route that served you.",
    start: "Get an API key",
  },
  footer: "© Axiom · Chinese text uses the MiSans font",
} satisfies Messages<typeof zh>;
