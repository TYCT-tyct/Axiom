import type { components } from "../packages/api/src/console.gen.ts";

type Schemas = components["schemas"];

export type BillMockState = {
  methods: Schemas["PaymentMethod"][];
  statementDaily: Schemas["StatementDay"][];
  subscriptions: Schemas["AccountSubscription"][];
  billingPreference: Schemas["BillingPreference"];
  redemptions: Schemas["RedeemCodeRedemption"][];
};

const usd = (units: string): Schemas["Money"] => ({ amount: `${units}000000000`, currency: "USD" });
const cny = (fen: string): Schemas["PayAmount"] => ({ amount: fen, currency: "CNY" });
const nano = (amount: string): Schemas["Money"] => ({ amount, currency: "USD" });

export function usualBillState(): BillMockState {
  return {
    methods: [
      { method: "alipay", total: cny("7373"), fee: cny("73") },
      { method: "wxpay", total: cny("7380"), fee: cny("80") },
    ],
    statementDaily: [
      {
        day: "2026-10-14",
        spent: nano("500000000"),
        topped_up: usd("10"),
        gifted: usd("0"),
        expired: usd("0"),
        closing_balance: nano("11750000000"),
      },
      {
        day: "2026-10-13",
        spent: nano("150000000"),
        topped_up: usd("0"),
        gifted: usd("0"),
        expired: usd("0"),
        closing_balance: null,
      },
    ],
    subscriptions: [
      {
        id: `sub_${"1".repeat(26)}`,
        plan_name: "Pro Monthly",
        state: "active",
        starts_at: "2026-10-01T00:00:00Z",
        ends_at: "2026-11-01T00:00:00Z",
        current_window: {
          seq: 1,
          starts_at: "2026-10-01T00:00:00Z",
          ends_at: "2026-11-01T00:00:00Z",
          quota: "50000000000",
          used: "12000000000",
          reserved: "250000000",
          remaining: "37750000000",
        },
      },
    ],
    billingPreference: {
      preference: "subscription_first",
      source: "site",
      site_default: "subscription_first",
    },
    redemptions: [
      {
        id: `rdm_${"1".repeat(26)}`,
        amount: usd("5"),
        code_hint: "7F9K",
        redeemed_at: "2026-10-10T10:00:00Z",
        expires_at: "2027-10-10T10:00:00Z",
      },
    ],
  };
}

export function emptyBillState(): Pick<BillMockState, "statementDaily" | "subscriptions" | "redemptions"> {
  return { statementDaily: [], subscriptions: [], redemptions: [] };
}

export function worstBillDaily(): Schemas["StatementDay"][] {
  return [
    {
      day: "2026-10-14",
      spent: nano("9223372036854775807"),
      topped_up: nano("9223372036854775807"),
      gifted: usd("0"),
      expired: usd("0"),
      closing_balance: null,
    },
  ];
}
