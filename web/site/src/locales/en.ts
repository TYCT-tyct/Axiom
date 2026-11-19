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
      setKey: "# model: a name from the key’s model list; the key is set in a terminal:",
      pickModel: "# pick a model and use it as usual",
      compatOpenai: "# OpenAI-compatible",
      compatAnthropic: "# Anthropic-compatible",
      hello: "Hello",
      yourKey: "your key",
      other: "Other tools",
      modelList: "# model: a name from the key’s model list (GET /v1/models)",
      copyTerminal: "Copy the terminal commands",
      copyFile: "Copy the configuration file",
      copyUrl: "Copy the base URL",
      copyCode: "Copy the code",
      copyEnv: "Copy the .env",
      copyUnavailable: "Cannot copy: this site’s API address is unavailable",
      noApiCard: "# this site’s API address is unavailable, so there is no configuration yet",
      copyFailed: "Copy failed; select it by hand",
      copied: "Copied",
      comma: ",",
      every: "and your models work",
      lede: "Point your tool’s API address at {{site}} and enter your key; the rest works as it did.",
    },
    faq: {
      a0: "One key calls models from many vendors (which ones is up to the key’s settings), with no separate account or top-up for each. Behind one model there can be several endpoints: {{site}} picks one by price and health by default, and when an endpoint times out, is rate-limited or fails upstream, it moves to the next eligible one if fallbacks are allowed and one remains. There is one bill.",
      a1: "An endpoint other than the official APIs we connect to directly (cloud providers included), usually a relay’s group shown under an anonymous code, and usually cheaper. To limit the sources, create a routing preset in the console (for example official sources only) and bind it to the key; changing the default preset does not change a key that already exists.",
      a2: "Claude Code, Codex, Cursor and the OpenAI SDK all work: set the address and the key as the connect screen shows for each, then pick a model from the key’s model list. So does any other tool that speaks the OpenAI or Anthropic API.",
      a3: "You pay by the endpoint that served the request, and each endpoint has its own billable items and unit prices: chat per input and output token; images per image on some endpoints and per token on others; web search and other tools per use. When the upstream does not report all the usage (a stream cut off midway, say), the missing part is charged as estimated; a charge that was estimated whole is recalculated later if it can be matched to the supplier’s full records. Where the site publishes prices, the models page lists each endpoint’s; where it does not, the console’s models page shows them once you sign in.",
      a4: "When an endpoint times out, is rate-limited or fails upstream, {{site}} retries on another one if fallbacks are allowed and an eligible one remains; a problem with the request itself (bad parameters, say) and a content-moderation refusal come straight back to you, and once content has started reaching you it does not switch. Attempts that failed before a switch are not charged to you; the last attempt is charged by how it ended: content already delivered to you is charged by actual usage, and per-use items that already ran (a search, say) are charged. The console’s call log shows every attempt.",
      a5: "Endpoint prices follow the site’s pricing rules: prices set as a markup on cost or on a reference price move with them; fixed prices usually stay, but rise when the cost passes the break-even line, and when that rise is more than the site lets through on its own, the model takes no new calls until the site confirms the new price. Each request is settled at the price locked in when it was sent, so a later change does not touch a request already sent.",
      a6: "Delete the key in the console and create a new one. Set a spending limit and an IP allowlist on every key in advance: a limit can reset daily, weekly or monthly, or be a total that never resets. A limit decides whether new calls may start: one that resets caps each period only, and a total that never resets lets no new call through once it is spent, though requests already sent and later usage corrections can still take the total spend past it. So delete a leaked key as soon as you can.",
      a7: {
        open: "Signing up is free. There is no trial credit for now: you pay by usage, only for what you use.",
        invite:
          "Signing up is free, though this site takes new accounts by invite code. There is no trial credit for now: you pay by usage, only for what you use.",
        closed:
          "This site is not taking new accounts for now; existing accounts sign in. There is no trial credit: you pay by usage, only for what you use.",
        unknown:
          "Signing up is free; whether this site takes new accounts, and whether it needs an invite code, the sign-up page says. There is no trial credit: you pay by usage, only for what you use.",
      },
      signUp: {
        open: "Free",
        invite: "Free, by invite code",
        closed: "Not open",
        unknown: "See the sign-up page",
      },
      billing: "By usage",
      envNote: "# the two APIs’ addresses; the key is set separately",
      sample: "The prices, amounts and keys in the picture are sample data.",
    },
    bed: {
      tagline: "One key for every model it may use.",
      join: {
        open: "Sign up and get an API key",
        invite: "Sign up with an invite code",
        signIn: "Sign in and get an API key",
      },
      lede: {
        open: "Sign up for a key, point your tools’ address and key at this site, and keep using the tools you use now.",
        invite:
          "Sign up with an invite code for a key, point your tools’ address and key at this site, and keep using the tools you use now.",
        signIn:
          "Sign in to create a key in the console, point your tools’ address and key at this site, and keep using the tools you use now.",
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
