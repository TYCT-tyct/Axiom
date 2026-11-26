import { redirect } from "@tanstack/react-router";
import { loadTranslatedPage } from "../locales/load-page";
import { paymentOrderState } from "../orders/states";
import { cashView } from "../overview";
import { redeemState } from "../redeem";
import { movedPage, shellPage, text } from "./shell";

// 用户与收入: users and their accounts, subscriptions, orders and refunds, and redeem codes.
const users = () => loadTranslatedPage(import("../pages/users"));
const usersSearch = (search: Record<string, unknown>) => ({ q: text(search.q), selected: text(search.selected) });
const subscriptions = () => loadTranslatedPage(import("../pages/subscriptions"));
const orders = () => loadTranslatedPage(import("../pages/orders"));
const ordersWindow = (search: Record<string, unknown>) => ({
  since: text(search.since),
  until: text(search.until),
  selected: text(search.selected),
});
const withInactive = (search: Record<string, unknown>) => ({
  include_inactive: search.include_inactive === true || search.include_inactive === "true" ? true : undefined,
});
const discrepancyState = (value: unknown) => (value === "open" || value === "resolved" ? value : undefined);

export const peopleRoutes = [
  shellPage("/users", users, usersSearch),
  shellPage("/users/accounts", users, usersSearch),
  shellPage("/users/roles", users, usersSearch),
  movedPage("/accounts/$accountId", ({ accountId }) => ({ to: "/users/accounts", search: { selected: accountId } })),
  shellPage("/subscriptions", subscriptions, usersSearch),
  shellPage("/subscriptions/plans", subscriptions, withInactive),
  movedPage("/subscription-plans", (_, search) => ({ to: "/subscriptions/plans", search }), withInactive),
  // The dashboard's drill-downs from before the tabs, /orders?cash=1 and /orders?paid=batches, with their window.
  shellPage(
    "/orders",
    orders,
    (search) => ({
      account_id: text(search.account_id),
      user_id: text(search.user_id),
      state: paymentOrderState(search.state),
      merchant_id: text(search.merchant_id),
      cash: cashView(search.cash),
      paid: text(search.paid),
      since: text(search.since),
      until: text(search.until),
      selected: text(search.selected),
    }),
    ({ cash, paid, since, until }) => {
      if (cash || paid === "batches") {
        throw redirect({ to: cash ? "/orders/cash" : "/orders/batches", search: { since, until } });
      }
    },
  ),
  shellPage("/orders/cash", orders, ordersWindow),
  shellPage("/orders/batches", orders, ordersWindow),
  shellPage("/orders/refunds", orders, ordersWindow),
  shellPage("/orders/discrepancies", orders, (search) => ({
    state: discrepancyState(search.state),
    selected: text(search.selected),
  })),
  movedPage(
    "/payment-discrepancies",
    (_, search) => ({ to: "/orders/discrepancies", search }),
    (search) => ({ state: discrepancyState(search.state) }),
  ),
  movedPage("/payment-discrepancies/$discrepancyId", ({ discrepancyId }) => ({
    to: "/orders/discrepancies",
    search: { selected: discrepancyId },
  })),
  shellPage(
    "/redeem-codes",
    () => loadTranslatedPage(import("../pages/redeem-codes")),
    (search) => ({ state: redeemState(search.state), note: text(search.note), selected: text(search.selected) }),
  ),
] as const;
