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
    connect: {
      terminal: "Terminal",
      cursorSettings: "Cursor settings → Models",
      alsoZshrc: "# or put it in ~/.zshrc",
      setKey: "# set the key in the terminal; model: a name from the key’s model list",
      pickModel: "# pick a model and use it as usual",
      compatOpenai: "# OpenAI-compatible",
      compatAnthropic: "# Anthropic-compatible",
      hello: "Hello",
      yourKey: "your key",
      other: "Other tools",
      copy: "Copy configuration",
      modelList: "# model: a name from the key’s model list (GET /v1/models)",
      copyUnavailable: "Cannot copy: this site’s API address is unavailable",
      copyFailed: "Copy failed; select the configuration by hand",
      copied: "Copied",
      comma: ",",
      every: "and your models work",
      lede: "Point your tool’s API address at {{site}} and enter your key. Your commands, code and workflows stay as they are.",
    },
    faq: {
      a0: "One key calls models from many vendors (which ones is up to the key’s settings), with no separate account or top-up for each. Behind one model there can be several endpoints: {{site}} picks one by price and health by default, and when fallbacks are allowed it moves to the next available one when one fails. There is one bill.",
      a1: "An endpoint the model’s maker does not run itself, usually cheaper. To limit the sources, create a routing preset in the console (for example official sources only) and bind it to the key; changing the default preset does not change a key that already exists.",
      a2: "Claude Code, Codex, Cursor and the OpenAI SDK all work: change the address and the key, then pick a model from the key’s model list. So does any other tool that speaks the OpenAI or Anthropic API.",
      a3: "You pay by the endpoint that served the request. Each endpoint has its own billable items and unit prices: chat is usually per input and output token, images per image, web search and other tools per use. Where the site publishes prices, the models page lists each endpoint’s; where it does not, they are visible after you sign in.",
      a4: "When fallbacks are allowed and another endpoint is available, {{site}} retries on it; once content has started reaching you it does not switch. Attempts that failed before a switch are not charged to you; the last attempt is charged by how it ended: content already delivered to you is charged by actual usage, and per-use items that already ran (a search, say) are charged. The console’s call log shows every attempt.",
      a5: "Endpoint prices follow the upstream. Each request is settled at the price locked in when it was sent, so a later change does not touch a request already sent; where prices are visible, the models page shows the current ones.",
      a6: "Delete the key in the console and create a new one. Set a spending limit and an IP allowlist on every key in advance, and a leak can only cost so much.",
      a7: {
        open: "Signing up is free. There is no trial credit for now: you pay by usage, only for what you use.",
        invite:
          "Signing up is free, though this site takes new accounts by invite code. There is no trial credit for now: you pay by usage, only for what you use.",
        closed:
          "This site is not taking new accounts for now; existing accounts sign in. There is no trial credit: you pay by usage, only for what you use.",
      },
      billing: "By usage",
      sample: "The prices, amounts and keys in the picture are sample data.",
    },
    bed: {
      tagline: "One key for every model it may use.",
      join: {
        open: "Sign up and get an API key",
        invite: "Sign up with an invite code",
        closed: "Sign in and get an API key",
      },
      lede: {
        open: "Sign up for a key, change two lines of configuration, and keep using the tools you use now.",
        invite:
          "Sign up with an invite code for a key, change two lines of configuration, and keep using the tools you use now.",
        closed:
          "Sign in to create a key in the console, change two lines of configuration, and keep using the tools you use now.",
      },
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
