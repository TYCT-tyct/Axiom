import first from "@axiom/boards/Site/regions/FirstScreen.en";
import headline from "@axiom/boards/Site/regions/Headline.en";
import wide from "@axiom/boards/Site/regions/Shell.en";
import narrow from "@axiom/boards/SitePhone/regions/Shell.en";
import type { Messages } from "@axiom/ui";
import type zh from "./zh";

export default {
  home: "Home",
  navigation: "Main navigation",
  models: "Models & prices",
  signIn: "Sign in",
  board: { ...wide, ...narrow, ...first, ...headline },
  language: "English",
  theme: {
    toDark: "Switch to dark",
    toLight: "Switch to light",
  },
  footer: "© Axiom · Chinese text uses the MiSans font",
} satisfies Messages<typeof zh>;
