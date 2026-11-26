import type { AdminSchemas } from "@axiom/api";
import { Text, useFormat } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { SubLine } from "../parts";
import { orderAmounts } from "./amounts";

type Order = AdminSchemas["PaymentOrder"];

// What the channel took for an order: paid_total, with the quote and the fee under it when they differ. An order known not
// to be paid says so; any other without a paid_total is a record this node does not have, which is not a zero.
export function PaidAmount({ order }: { order: Order }) {
  const { t } = useTranslation();
  const format = useFormat();
  const { paid, paidFee, quote, paidMissing } = orderAmounts(order);
  return (
    <>
      {paid ? (
        format.pay(paid)
      ) : (
        <Text as="span" size="sm" tone="faint">
          {t(paidMissing ? "orders.paidUnavailable" : "orders.notPaid")}
        </Text>
      )}
      {quote ? (
        <SubLine>
          {t("orders.quote")} {format.pay(quote)}
        </SubLine>
      ) : null}
      {paidFee ? (
        <SubLine>
          {t("orders.fee")} {format.pay(paidFee)}
        </SubLine>
      ) : null}
    </>
  );
}

// The credit an order added, `credited`, with the quoted credits under it when they differ; without it, "not credited" for
// an order known unpaid and "not recorded" for any other.
export function CreditedAmount({ order }: { order: Order }) {
  const { t } = useTranslation();
  const format = useFormat();
  const { credited, quotedCredits, creditedMissing } = orderAmounts(order);
  return (
    <>
      {credited ? (
        format.money(credited)
      ) : (
        <Text as="span" size="sm" tone="faint">
          {t(creditedMissing ? "orders.creditedUnavailable" : "orders.notCredited")}
        </Text>
      )}
      {quotedCredits ? (
        <SubLine>
          {t("orders.quote")} {format.money(quotedCredits)}
        </SubLine>
      ) : null}
    </>
  );
}
