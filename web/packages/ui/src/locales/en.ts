import type { Messages } from "../i18n";
import type zh from "./zh";

export default {
  brand: "Axiom",
  preferences: {
    theme: "Appearance",
    themes: { system: "System", light: "Light", dark: "Dark" },
    language: "Language",
  },
  shell: {
    openMenu: "Open menu",
    closeMenu: "Close menu",
    navigation: "Main navigation",
    skipToContent: "Skip to content",
  },
  state: {
    loading: "Loading",
    emptyTitle: "Nothing here yet",
    errorTitle: "Couldn’t load this",
    errorBody: "Try again in a moment. If it keeps failing, include the request ID below when you contact support.",
    networkError: "Can’t reach Axiom. Check your connection and try again.",
    requestId: "Request ID {{id}}",
    failedAt: "Failed at {{time}}",
    retry: "Try again",
    forbiddenTitle: "No access",
    forbiddenBody: "Your account can’t view this. Ask an admin of the workspace if you need it.",
    pageFailed: "This page didn’t load",
    pageFailedBody: "The network may have dropped, or Axiom was just updated. Reload the page to try again.",
    reload: "Reload page",
    notFoundTitle: "Page not found",
    notFoundBody: "The link may be wrong, or the page has moved.",
    home: "Go home",
  },
} satisfies Messages<typeof zh>;
