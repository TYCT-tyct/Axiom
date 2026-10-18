import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { parseMoney, TextField, useFormat } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";

type Grant = { amount: AdminSchemas["Money"] };

// Adds a gift of credit to an account: the amount in the account's currency. The credit does not expire (the server
// refuses an expiry until credit is written off at its end). The money limit of one grant is the server's to check.
export function CreditDialog({ account, onClose }: { account: AdminSchemas["AccountDetail"]; onClose: () => void }) {
  const { t } = useTranslation();
  const currency = account.balance.balance.currency;
  const read = (values: Record<string, string>): Grant | Problem => {
    const amount = parseMoney(values.amount ?? "");
    if (amount === null || /^0+$/.test(amount)) {
      return { problem: t("users.credit.amountInvalid") };
    }
    return { amount: { amount, currency } };
  };
  return (
    <ChangeDialog
      title={t("users.credit.title", { email: account.owner.email })}
      intro={<p>{t("users.credit.intro")}</p>}
      action={t("users.credit.submit")}
      fields={() => <CreditFields currency={currency} />}
      read={read}
      send={(grant, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/accounts/{account_id}/credit-grants", {
            params: { path: { account_id: account.id }, header: { "Idempotency-Key": key } },
            body: { ...grant, reason },
          }),
        )
      }
      done={(granted) => <Granted account={account} granted={granted} />}
      onClose={onClose}
    />
  );
}

function CreditFields({ currency }: { currency: string }) {
  const { t } = useTranslation();
  return (
    <TextField
      label={t("users.credit.amount", { currency })}
      hint={t("users.credit.amountHint")}
      name="amount"
      inputMode="decimal"
      autoComplete="off"
    />
  );
}

function Granted({
  account,
  granted,
}: {
  account: AdminSchemas["AccountDetail"];
  granted: AdminSchemas["CreditGrant"];
}) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <>
      <p className="text-t1">
        {t("users.credit.granted", { email: account.owner.email, amount: format.money(granted.amount) })}
      </p>
      <p>
        {t("users.credit.balanceAfter", {
          balance: format.money(granted.balance_after.balance),
          available: format.money(granted.balance_after.available),
        })}
      </p>
      <p className="text-label text-t3">
        {t("users.credit.grantId")}
        {": "}
        <span className="font-mono">{granted.id}</span>
      </p>
    </>
  );
}
