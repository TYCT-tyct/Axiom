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
  sections: {
    surface: "Surface",
    connect: "Connect",
    models: "Models and routes",
    steps: "Three steps",
    production: "Before production",
    faq: "FAQ",
    seabed: "Seabed",
  },
  theme: {
    toDark: "Switch to dark",
    toLight: "Switch to light",
  },
  script: {
    models: {
      routes_one: "{{count}} route",
      routes_other: "{{count}} routes",
      timeout: "Timed out",
      current: "Chosen",
      switched: "Switched to",
      cheapest: "Lowest price",
      failed: "Route {{route}} timed out; the request was retried",
      say: "{{model}}, route {{route}} chosen",
      unit: "Prices in {{unit}}, per million tokens",
      noPrice: "Sign in to see this site's prices",
      sampleTokens: "4.2B",
      sample: "sample data",
      emptyTitle: "No models are listed yet",
      emptyBody: "Once they are, the models and their routes show here.",
    },
    steps: {
      noKey: "No key yet",
      creating: "Creating",
      created: "Created",
      copy: "Copy",
      copied: "Copied",
      justNow: "just now",
      editing: "Editing",
      saved: "Saved",
      waitSend: "Waiting to send",
      waitReply: "Waiting for reply",
      ok: "OK",
    },
    trust: {
      inUse: "In use",
      timeout: "timed out",
      free: "Not charged",
      calling: "Calling normally",
      stopped: "Limit reached, calls stopped",
    },
  },
  footer: "© Axiom · Chinese text uses the MiSans font",
} satisfies Messages<typeof zh>;
