export default {
  description:
    "What the site sets for the sessions and keys its users get. A setting left empty is the default in code, so only what the site chooses differently is recorded here.",
  title: "Sessions and keys",
  own: "Site setting",
  default: "Default",
  raw: "({{seconds}} s)",
  edit: "Edit settings",
  intro:
    "A setting left empty goes back to the default. Shorter sessions take effect on the next request of every session. The change goes to the audit log.",
  save: "Save settings",
  saved: "The settings are saved.",
  field: {
    console_idle_timeout_seconds: "Console: idle time before a session ends",
    console_max_age_seconds: "Console: longest a session lasts",
    admin_idle_timeout_seconds: "Admin: idle time before a session ends",
    admin_max_age_seconds: "Admin: longest a session lasts",
    new_key_daily_limit: "Daily spend limit of a new key",
  },
  hint: "In seconds, from {{min}} to {{max}}. Empty uses the default ({{default}}).",
  limitHint: "An amount in {{currency}}, 0 to 1,000,000, up to 9 decimals. Empty uses the default.",
  invalidRange: "Times are whole seconds within the range shown.",
  invalidLimit: "The daily limit of a new key is an amount in {{currency}} from 0 to 1,000,000, with up to 9 decimals.",
  keyLimits: {
    title: {
      site: "Key limits of the site's accounts",
      account: "Key limits of this account",
    },
    field: {
      max_keys: "Call keys it may hold",
      creations_per_hour: "Call keys it may make in an hour",
    },
    own: "Set here",
    from: { site: "Default", account: "From the site" },
    edit: "Edit key limits",
    save: "Save key limits",
    saved: "The key limits are saved.",
    intro: {
      site: "What every account of the site is held to unless it has limits of its own. A limit left empty goes back to the default in code. Lowering one deletes no key: an account over it only cannot make more. The change goes to the audit log.",
      account:
        "This account's own limits, over the site's. A limit left empty goes back to the site's. Lowering one deletes no key: the account only cannot make more. The change goes to the audit log.",
    },
    hint: {
      site: "A whole number from {{min}} to {{max}}. Empty uses the default in code.",
      account: "A whole number from {{min}} to {{max}}. Empty uses the site's.",
    },
    invalid: "Limits are whole numbers from {{min}} to {{max}}.",
  },
};
