import type { Messages } from "@axiom/ui";
import type zh from "../zh";
import alertConfig from "./alert-configuration";
import alertEvents from "./alert-events";
import announcements from "./announcements";
import approvals from "./approvals";
import audit from "./audit";
import auditEvents from "./audit-events";
import calls from "./calls";
import exchangeRates from "./exchange-rates";
import legalDocs from "./legal-documents";
import me from "./me";
import models from "./models";
import operations from "./operations";
import orders from "./orders";
import discrepancies from "./payment-discrepancies";
import platformGrants from "./platform-grants";
import plugins from "./plugins";
import pricePolicy from "./price-policy";
import pricing from "./pricing";
import rateLimits from "./rate-limits";
import redeem from "./redeem";
import routingPresets from "./routing-presets";
import settingWords from "./setting-words";
import settings from "./settings";
import shell from "./shell";
import sourceClasses from "./source-classes";
import subscriptions from "./subscriptions";
import suppliers from "./suppliers";
import upstream from "./upstream";
import basePrice from "./upstream-base-price";
import channel from "./upstream-channel";
import users from "./users";

export const initial = {
  brandName: "Axiom Admin",
  forbiddenHint: "Ask the site owner to grant the permission.",
  nav: {
    users: "Users",
    orders: "Orders",
    discrepancies: "Payment discrepancies",
    redeemCodes: "Redeem codes",
    calls: "Calls",
    audit: "Audit log",
    models: "Models",
    pricing: "Pricing",
    subscriptionPlans: "Subscription plans",
    alertEvents: "Alert events",
    announcements: "Announcements",
  },
  shell,
  drawerSteps: { previous: "Previous", next: "Next" },
  signIn: { description: "The admin is for operators. After signing in you also complete two-step verification." },
  overview: {
    targets: {
      month: "Target month",
      monthHint: "Months are UTC calendar months. Times on this page are shown in your time zone ({{zone}}).",
      edit: "Edit targets",
      title: "Targets for {{month}}",
      intro:
        "Set this month's targets. A field left empty clears that target; the dashboard's goals set revenue, paying accounts, margin and retention against the month so far.",
      calls: "Calls",
      callsHint: "At least this many calls in the month.",
      errors: "Errors",
      errorsHint: "At most this many errors in the month.",
      revenue: "Revenue ({{currency}})",
      revenueHint: "At least this much revenue in the month.",
      cost: "Supplier cost ({{currency}})",
      costHint: "At most this much supplier cost in the month.",
      paidUsers: "Paid users",
      paidUsersHint: "At least this many accounts with a paid top-up in the month.",
      monthlyRevenue: "Monthly revenue ({{currency}})",
      monthlyRevenueHint: "At least this much credit from paid top-ups in the month; not call revenue.",
      grossMargin: "Gross margin (%)",
      grossMarginHint: "A percentage of call revenue, from 0 to 100.",
      retention: "Retention (%)",
      retentionHint: "A percentage of last month's paying accounts that pay again this month.",
      countInvalid: "Enter a whole number, or leave it empty to clear the target.",
      moneyInvalid: "Enter an amount like 1200.50 with at most 38 digits, or leave it empty to clear the target.",
      rateInvalid: "Enter a percentage from 0 to 100 with at most two decimals, or leave it empty to clear the target.",
      save: "Save targets",
      saved: "The targets for this month are saved.",
    },
  },
  list: { noMatch: { title: "No record matches", body: "Try another word, or clear the filter." } },
  filter: { apply: "Filter", reset: "Clear" },
  change: {
    reason: "Reason",
    reasonHint: "Why you are doing this. It goes to the audit log and can be looked up later.",
    reasonMissing: "Enter a reason.",
    reasonPlaceholder: "For whoever reads the audit log later",
    picksLabel: "Common reasons",
    picks: {
      routine: "Routine maintenance",
      request: "User request",
      incident: "Incident response",
      price: "Price adjustment",
      risk: "Risk control",
    },
    replayedToast: "Already in effect; nothing was done twice",
    undo: "Undo",
    undoReason: "Undo: {{reason}}",
    undone: "Undone",
    undoRefused: "Not undone: {{message}}",
    undoUnknown: "It isn’t known whether the undo took effect. Check the result on the page before undoing again.",
    confirmLabel: "Type “{{name}}” to confirm",
    confirmMismatch: "What you typed doesn’t match the thing you are changing.",
    done: "Done.",
    replayed: "This was already done earlier. Below is the result from then; nothing was done again.",
    close: "Close",
    unknown:
      "It isn’t known whether this change was made. The form is locked: resending sends the same request, which the server does at most once — the surest way to learn the result. If you close this, the request is kept: when you open this change again you can send it again or give it up. The data on the page does not prove it wasn’t made.",
    unsettled:
      "A change you made here earlier got no answer, so it isn’t known whether it was made. Send it again as it was — the server does it at most once, and tells you if it was already done — or give it up. If you give it up, it may still have taken effect: check the data before making the change again, because the data on the page does not prove it wasn’t made.",
    giveUp: "Give it up",
    resend: "Resend as it was",
    stale:
      "Someone else changed this after you opened it. Nothing was saved, so their change isn’t overwritten. Reloading fills in the current version and discards what you entered here.",
    reload: "Reload",
    reused:
      "This request’s key was already used for different content, so the first attempt may have been made; nothing more is sent from here. The data you see after closing may not include it yet, so it can’t tell you whether to do it again.",
  },
  drafts: {
    pending_one: "{{count}} change not submitted",
    pending_other: "{{count}} changes not submitted",
    invalid_one: "{{count}} value is not valid; fix it to submit",
    invalid_other: "{{count}} values are not valid; fix them to submit",
    discard: "Discard",
    discarded: "Changes discarded",
    review: "Review and submit",
    title_one: "Submit {{count}} change",
    title_other: "Submit {{count}} changes",
    intro:
      "Changes take effect at once. Settings saved apart go out one group after another; a group that doesn’t go through stays on the page to fix and submit again.",
    action: "Submit",
    submitted_one: "{{count}} change submitted",
    submitted_other: "{{count}} changes submitted",
    done: "Submitted.",
    went: "Submitted: {{names}}",
    failed: "Not submitted: {{names}}. {{message}}",
    waiting: "Not sent yet: {{names}}",
    moved:
      "Someone else changed {{names}} after you did. So as not to overwrite that, revert these, look at what is current and change them again.",
    revert: "Revert",
    revertName: "Revert “{{name}}”",
    on: "On",
    off: "Off",
    none: "None",
    remove: "Remove {{value}}",
    leaveTitle: "Changes not submitted",
    leaveBody: "If you leave this page, the changes you haven’t submitted are lost.",
    leave: "Leave",
    stay: "Stay",
    secretSet: "Set",
    secretUnset: "Not set",
    secretReplace: "Replace",
    secretNew: "New secret",
    default: "Default",
    noLimit: "No limit",
  },
  models,
  pricing,
  approvals,
} satisfies Messages<
  Omit<
    typeof zh,
    | "operations"
    | "setting"
    | "scope"
    | "unit"
    | "settings"
    | "subscriptions"
    | "suppliers"
    | "upstream"
    | "discrepancies"
    | "exchangeRates"
    | "platformGrants"
    | "pricePolicy"
    | "alertConfig"
    | "alertEvents"
    | "announcements"
    | "legalDocs"
    | "rateLimits"
    | "sourceClasses"
    | "routingPresets"
    | "plugins"
    | "auditEvents"
    | "calls"
    | "audit"
    | "me"
    | "users"
    | "orders"
    | "redeem"
  >
>;

// The complete catalog is for types and tooling; the app imports only initial.
export default /* @__PURE__ */ Object.assign({}, initial, settingWords, {
  operations,
  settings,
  subscriptions,
  suppliers,
  upstream: /* @__PURE__ */ Object.assign({}, upstream, channel, basePrice),
  sourceClasses,
  routingPresets,
  discrepancies,
  exchangeRates,
  platformGrants,
  pricePolicy,
  alertConfig,
  alertEvents,
  announcements,
  legalDocs,
  rateLimits,
  plugins,
  me,
  auditEvents,
  calls,
  audit,
  users,
  orders,
  redeem,
}) satisfies Messages<typeof zh>;
