import type { Messages } from "@axiom/ui";
import type zh from "../zh";
import approvals from "./approvals";
import audit from "./audit";
import calls from "./calls";
import mappings from "./mappings";
import models from "./models";
import operations from "./operations";
import orders from "./orders";
import pricing from "./pricing";
import redeem from "./redeem";
import routingPresets from "./routing-presets";
import settings from "./settings";
import sourceClasses from "./source-classes";
import suppliers from "./suppliers";
import users from "./users";

export const initial = {
  brandName: "Axiom Admin",
  forbiddenHint: "Ask the site owner to grant the permission.",
  nav: {
    overview: "Overview",
    approvals: "Approvals",
    operationsCredentials: "Operations credentials",
    operationsLimits: "Money & execution limits",
    users: "Users",
    orders: "Orders",
    redeemCodes: "Redeem codes",
    calls: "Calls",
    audit: "Audit log",
    suppliers: "Suppliers",
    mappings: "Unmapped models",
    models: "Model catalog",
    pricing: "Pricing",
    settings: "Site settings",
    sourceClasses: "Source classes",
    routingPresets: "Routing presets",
    more: "More",
  },
  navSections: {
    overview: "Operations overview",
    users: "Users and accounts",
    supply: "Suppliers and lines",
    catalog: "Model catalog",
    pricing: "Pricing and plans",
    routing: "Routing and sources",
    payments: "Payments and reconciliation",
    operations: "Operations and approvals",
    alerts: "Alerts",
    site: "Sites",
    platform: "Platform settings",
    calls: "Call records",
    audit: "Audit log",
    security: "Sign-in and security",
  },
  shell: { collapse: "Collapse", expand: "Expand" },
  calls,
  audit,
  sourceClasses,
  routingPresets,
  signIn: { description: "The admin is for operators. After signing in you also complete two-step verification." },
  home: {
    title: "Operations",
    description:
      "This site's calls, revenue, cost, cash and paying customers in the chosen window. Every figure opens the calls, orders or cash behind it.",
    noAccessTitle: "No page is available to you",
    noAccessBody: "Your account holds no admin permission. Ask the owner to grant one.",
  },
  overview: {
    rangeLabel: "Time range",
    range: { "24h": "24 hours", "7d": "7 days", "30d": "30 days" },
    window: "{{since}} to {{until}}",
    calls: "Calls",
    errors: "Errors",
    revenue: "Revenue",
    revenueNote: "Charged to users, less what the platform bore",
    cost: "Supplier cost",
    costNote: "Cost booked where billing could convert it",
    unknownCost: "{{calls}} calls have no known cost and are not in the total",
    callsIncomplete:
      "Calls before {{since}} are archived: the call counts and rankings omit them. Revenue, cost, cash and the paid-top-up figures still cover the whole window.",
    callsIncompleteUnknown:
      "Calls before this node's retained log are archived: the call counts and rankings are incomplete. Revenue, cost, cash and the paid-top-up figures still cover the whole window.",
    coverageUnavailable:
      "This node cannot say whether older calls were archived, so the call counts and rankings may be incomplete.",
    cash: "Cash received",
    cashNote: "Paid credit less refunds; not call revenue",
    business: "Business",
    paidUsers: "Paid users",
    paidUsersNote: "Accounts with a paid top-up in the window",
    monthlyRevenue: "Monthly revenue",
    monthlyRevenueNote: "Credit granted by paid top-ups; not call revenue",
    grossMargin: "Gross margin",
    grossMarginNote: "Call revenue less supplier cost, where the cost total is complete",
    retention: "Retention",
    retentionNote:
      "{{retained}} of the {{cohort}} accounts that paid in the previous month paid again by the window's end",
    retentionEmpty: "No account paid in the previous month, so there is no cohort to measure",
    retentionUnavailable: "Retention is not available on this node",
    targets: {
      description:
        "Targets cover one UTC calendar month. Counts and money are prorated to the window you pick; margin and retention use the month the window ends in.",
      month: "Target month",
      monthHint: "Months are UTC calendar months. Times on this page are shown in your time zone ({{zone}}).",
      edit: "Edit targets",
      title: "Targets for {{month}}",
      intro:
        "Set this month's targets. A field left empty clears that target; the dashboard compares the others with the actual figures, prorating counts and money and taking margin and retention from the month the window ends in.",
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
      target: "Target {{value}}",
      none: "No target set",
      empty: "No business target is set for this window. Set one for the target month above.",
      unavailable: "Business targets are not available on this node.",
      incomplete: "Gap not shown while calls are incomplete",
      costIncomplete: "Gap not shown while supplier cost is incomplete",
      marginIncomplete: "Gap not shown while revenue is zero or supplier cost is incomplete",
      retentionIncomplete: "Gap not shown without a previous-month cohort",
      cashNoTarget: "Cash has no target: it is kept per payment currency.",
      at: "At target",
      ahead: "{{amount}} above target",
      behind: "{{amount}} below target",
      over: "{{amount}} over target",
      under: "{{amount}} under target",
    },
    unavailable: "Not available on this node",
    topAccounts: "Busiest accounts",
    topModels: "Busiest models",
    col: { account: "Account", model: "Model", calls: "Calls", errors: "Errors" },
    noAccounts: { title: "No account called in this window", body: "Widen the time range." },
    noModels: { title: "No model was called in this window", body: "Widen the time range." },
  },
  filter: { apply: "Filter", reset: "Clear" },
  change: {
    reason: "Reason",
    reasonHint: "Why you are doing this. It goes to the audit log and can be looked up later.",
    reasonMissing: "Enter a reason.",
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
  users,
  orders,
  mappings,
  models,
  pricing,
  redeem,
  approvals,
} satisfies Messages<Omit<typeof zh, "operations" | "settings" | "suppliers">>;

// The complete catalog is for types and tooling; the app imports only initial.
export default /* @__PURE__ */ Object.assign({}, initial, { operations, settings, suppliers }) satisfies Messages<
  typeof zh
>;
