import type { Messages } from "@axiom/ui";
import type zh from "../zh/settings-pages";

export default {
  site: {
    lede: "The site’s name, logo and brand colour, whether prices are public, and which funds are used first.",
    brand: "Brand",
    brandLede: "How people see the site on the sign-in page, in the console and in emails",
    billing: "Billing and display",
    about: "About",
    id: "Site ID",
    idDetail: "Needed when you contact support or connect an operations agent",
    copyId: "Copy the site ID",
  },
  signIn: {
    lede: "What people can sign in with.",
    domain: "Domain and passkeys",
    providers: "Third-party sign-in",
    providersLede: "People can sign in with these accounts directly; an account is made the first time",
  },
  registration: {
    lede: "Who can sign up, and how bulk sign-ups are held back.",
    entry: "Sign-up",
    domains: "Email domains",
    domainsLede: "Limit bulk sign-ups from one domain, or allow only the emails of given domains",
  },
  sessions: {
    lede: "How long a sign-in lasts, and what happens after too many sign-in attempts.",
    keep: "How long a sign-in lasts",
    attempts: "Sign-in attempt limits",
    attemptsLede: "Too many attempts from one source are refused for a while",
  },
  limits: {
    lede: "These are the defaults for every account. To relax one account, change it under Users, Accounts.",
    keys: "Keys",
    workspaces: "Workspaces",
  },
  payments: {
    lede: "Whether people can top up, the range of one top-up, and the merchant accounts that take the money.",
    top: "Top-ups",
  },
  mail: {
    lede: "Which server the emails are sent from.",
    service: "Mail service",
    serviceLede: "Verification codes, balance reminders, alerts and plan-expiry notices are all sent from here",
  },
  gateway: {
    lede: "The numbers for reserving credit, quality checks and how long the usage log is kept.",
    reserve: "Credit reserve",
    reserveLede: "Before a request ends, part of the credit is frozen so a balance is not overspent",
    probes: "Quality checks",
    probesLede: "Real requests check every endpoint now and then, so a swapped or downgraded model is found",
    log: "Usage log",
  },
  agent: {
    lede: "The program that does routine inspection, customer compensation and price suggestions. Its permission ceiling is set here.",
    approvals: "See approvals",
    execution: "Execution limits",
    range: "Price range",
    rangeLede: "A sale price the agent proposes has to fall within this range",
  },
} satisfies Messages<typeof zh>;
