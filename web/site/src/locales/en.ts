import first from "@axiom/boards/Site/regions/FirstScreen.en";
import headline from "@axiom/boards/Site/regions/Headline.en";
import wide from "@axiom/boards/Site/regions/Shell.en";
import narrow from "@axiom/boards/SitePhone/regions/Shell.en";
import type { Messages } from "@axiom/ui";
import type zh from "./zh";

export default {
  home: "{{site}} Home",
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
    noApi: "This site's API address is unavailable",
    faq: {
      // after the answers whose picture shows figures: they are the board's illustration, not the site's
      sample: "The prices, amounts and keys in the picture are sample data.",
    },
    models: {
      routes_one: "{{count}} route",
      routes_other: "{{count}} routes",
      timeout: "Timed out",
      current: "Chosen",
      switched: "Would switch to",
      cheapest: "Lowest price",
      failed: "Demo: if route {{route}} timed out, the request would be retried",
      say: "{{model}}, route {{route}} chosen",
      unit: "Prices in {{unit}}, per million tokens",
      noPrice: "Sign in to see this site's prices",
      sampleTokens: "4.2B",
      sample: "sample data",
      unavailable: "Unavailable",
      listLabel: "Models",
      noneInFilter: "No models from this developer yet",
      emptyTitle: "No models are listed yet",
      emptyBody: "Once they are, the models and their routes show here.",
    },
    steps: {
      sample: "The key, timings and amounts in the demo are sample data.",
      noKey: "No key yet",
      creating: "Creating",
      created: "Created",
      copy: "Copy",
      copied: "Copied",
      justNow: "just now",
      editing: "Editing",
      saved: "Saved",
      noApi: "This site's API address is unavailable",
      waitSend: "Waiting to send",
      waitReply: "Waiting for reply",
      ok: "OK",
    },
    trust: {
      sample: "The prices, bill and limit in the demo are sample data.",
      ledgerNote:
        "Model, route, tokens and amount are recorded request by request, can be filtered by key and exported as CSV. A request that fails before any output reaches you is not charged for model usage.",
      inUse: "In use",
      timeout: "timed out",
      free: "Not charged",
      calling: "Calling normally",
      stopped: "Limit reached, calls stopped",
    },
  },
  footer: "© Axiom · Chinese text uses the MiSans font",
} satisfies Messages<typeof zh>;
