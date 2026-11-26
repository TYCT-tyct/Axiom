import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { DiffRows, Field, Mono, parseMoney, Select, Text, useFormat } from "@axiom/ui";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "./change";
import { paymentCurrencies, readCredit, shownBalance } from "./credit-read";
import { NewButton, Time } from "./parts";

// The button that gives an account credit, shown to a session that holds credits.grant.
export function GrantButton({ account }: { account: AdminSchemas["AccountDetail"] }) {
  const { t } = useTranslation();
  return (
    <NewButton permission="credits.grant" label={t("users.credit.open")}>
      {(close) => <CreditDialog account={account} onClose={close} />}
    </NewButton>
  );
}

// Adds credit to an account: a gift of an amount in the account's currency, or money the administrator received
// offline, in the currency it was paid in, which the server converts at the reference rate in force; optionally when
// what is left of it stops counting. The money limit of one grant is the server's to check. Nothing takes credit back,
// so the dialog says it cannot be undone, and shows the balance it will leave while the amount is typed.
function CreditDialog({ account, onClose }: { account: AdminSchemas["AccountDetail"]; onClose: () => void }) {
  const { t } = useTranslation();
  const currency = account.balance.balance.currency;
  const words = {
    amountInvalid: t("users.credit.amountInvalid"),
    expiresInvalid: t("users.credit.expiresInvalid"),
    channelInvalid: t("users.credit.channelInvalid"),
  };
  return (
    <ChangeDialog
      operation={`credit-grant:${account.id}`}
      title={t("users.credit.title", { email: account.owner.email })}
      intro={<p>{t("users.credit.intro")}</p>}
      warn={t("users.credit.final")}
      action={t("users.credit.submit")}
      fields={(error) => <CreditFields balance={account.balance.balance} error={error} />}
      read={(values) => readCredit(values, currency, words)}
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

function CreditFields({ balance, error }: { balance: AdminSchemas["Money"]; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [source, setSource] = useState("gift");
  const [paidIn, setPaidIn] = useState(balance.currency);
  const [typed, setTyped] = useState("");
  const offline = source === "offline_payment";
  return (
    <>
      <Select
        label={t("users.credit.source")}
        hint={t(offline ? "users.credit.offlineHint" : "users.credit.giftHint")}
        name="source"
        value={source}
        onChange={(event) => setSource(event.target.value)}
      >
        <option value="gift">{t("users.credit.gift")}</option>
        <option value="offline_payment">{t("users.credit.offline")}</option>
      </Select>
      {offline ? <PaidCurrency value={paidIn} onChange={setPaidIn} error={error} /> : null}
      <Field
        label={t(offline ? "users.credit.paid" : "users.credit.amount", {
          currency: offline ? paidIn : balance.currency,
        })}
        hint={t(offline ? "users.credit.paidHint" : "users.credit.amountHint")}
        name="amount"
        inputMode="decimal"
        autoComplete="off"
        value={typed}
        onChange={(event) => setTyped(event.target.value)}
        error={errors.field(error, offline ? "payment/amount" : "amount")}
      />
      <BalancePreview balance={balance} typed={typed} paidIn={offline ? paidIn : balance.currency} />
      {offline ? <OfflineChannel error={error} /> : null}
      <Field
        label={t("users.credit.expires")}
        hint={t("users.credit.expiresHint")}
        name="expires_at"
        type="datetime-local"
      />
    </>
  );
}

// What an offline payment was paid in, and where it was received: the two things a gift does not have.
function PaidCurrency({ value, onChange, error }: { value: string; onChange: (code: string) => void; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <Select
      label={t("users.credit.currency")}
      name="currency"
      value={value}
      onChange={(event) => onChange(event.target.value)}
      error={errors.field(error, "payment/amount/currency")}
    >
      {paymentCurrencies.map((code) => (
        <option key={code} value={code}>
          {code}
        </option>
      ))}
    </Select>
  );
}

function OfflineChannel({ error }: { error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <Field
      label={t("users.credit.channel")}
      hint={t("users.credit.channelHint")}
      name="channel"
      maxLength={200}
      autoComplete="off"
      error={errors.field(error, "payment/channel")}
    />
  );
}

// The balance the grant leaves, from the amount as typed: a gift, or a payment in the account's own currency, adds what
// it says. A payment in another currency is converted by the server at the rate in force, so only the balance now is
// known and the page says so.
function BalancePreview({ balance, typed, paidIn }: { balance: AdminSchemas["Money"]; typed: string; paidIn: string }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (parseMoney(typed) === null) {
    return <Text tone="muted">{t("users.credit.balanceNow", { balance: format.money(balance) })}</Text>;
  }
  if (paidIn !== balance.currency) {
    return <Text tone="muted">{t("users.credit.converted")}</Text>;
  }
  const after = shownBalance(balance, typed);
  return (
    <DiffRows
      rows={
        after
          ? [{ label: t("users.credit.balanceLabel"), before: format.money(balance), after: format.money(after) }]
          : []
      }
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
      <Text>{t("users.credit.granted", { email: account.owner.email, amount: format.money(granted.amount) })}</Text>
      {granted.payment ? (
        <p>
          {t("users.credit.paidVia", { paid: format.money(granted.payment.amount), channel: granted.payment.channel })}
          {granted.exchange_rate ? ` ${t("users.credit.rateUsed", { ...granted.exchange_rate })}` : null}
        </p>
      ) : null}
      <p>
        {t("users.credit.balanceAfter", {
          balance: format.money(granted.balance_after.balance),
          available: format.money(granted.balance_after.available),
        })}
      </p>
      <Text size="sm" tone="faint">
        {t("users.credit.grantId")}
        {": "}
        <Mono>{granted.id}</Mono>
        {granted.expires_at ? (
          <>
            {" · "}
            {t("users.credit.expiresOn")}
            {": "}
            <Time at={granted.expires_at} />
          </>
        ) : null}
      </Text>
    </>
  );
}
