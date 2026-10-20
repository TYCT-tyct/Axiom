import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { SelectField, TextField, useFormat } from "@axiom/ui";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "./change";
import { paymentCurrencies, readCredit } from "./credit-read";
import { Time } from "./parts";

// Adds credit to an account: a gift of an amount in the account's currency, or money the administrator received
// offline, in the currency it was paid in, which the server converts at the reference rate in force; optionally when
// what is left of it stops counting. The money limit of one grant is the server's to check.
export function CreditDialog({ account, onClose }: { account: AdminSchemas["AccountDetail"]; onClose: () => void }) {
  const { t } = useTranslation();
  const currency = account.balance.balance.currency;
  const words = {
    amountInvalid: t("users.credit.amountInvalid"),
    expiresInvalid: t("users.credit.expiresInvalid"),
    channelInvalid: t("users.credit.channelInvalid"),
  };
  return (
    <ChangeDialog
      title={t("users.credit.title", { email: account.owner.email })}
      intro={<p>{t("users.credit.intro")}</p>}
      action={t("users.credit.submit")}
      fields={(error) => <CreditFields currency={currency} error={error} />}
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

function CreditFields({ currency, error }: { currency: string; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [source, setSource] = useState("gift");
  const [paidIn, setPaidIn] = useState(currency);
  const offline = source === "offline_payment";
  return (
    <>
      <SelectField
        label={t("users.credit.source")}
        hint={t(offline ? "users.credit.offlineHint" : "users.credit.giftHint")}
        name="source"
        value={source}
        onChange={(event) => setSource(event.target.value)}
      >
        <option value="gift">{t("users.credit.gift")}</option>
        <option value="offline_payment">{t("users.credit.offline")}</option>
      </SelectField>
      {offline ? (
        <SelectField
          label={t("users.credit.currency")}
          name="currency"
          value={paidIn}
          onChange={(event) => setPaidIn(event.target.value)}
          error={errors.field(error, "payment/amount/currency")}
        >
          {paymentCurrencies.map((code) => (
            <option key={code} value={code}>
              {code}
            </option>
          ))}
        </SelectField>
      ) : null}
      <TextField
        label={t(offline ? "users.credit.paid" : "users.credit.amount", { currency: offline ? paidIn : currency })}
        hint={t(offline ? "users.credit.paidHint" : "users.credit.amountHint")}
        name="amount"
        inputMode="decimal"
        autoComplete="off"
        error={errors.field(error, offline ? "payment/amount" : "amount")}
      />
      {offline ? (
        <TextField
          label={t("users.credit.channel")}
          hint={t("users.credit.channelHint")}
          name="channel"
          maxLength={200}
          autoComplete="off"
          error={errors.field(error, "payment/channel")}
        />
      ) : null}
      <TextField
        label={t("users.credit.expires")}
        hint={t("users.credit.expiresHint")}
        name="expires_at"
        type="datetime-local"
      />
    </>
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
      <p className="text-label text-t3">
        {t("users.credit.grantId")}
        {": "}
        <span className="font-mono">{granted.id}</span>
        {granted.expires_at ? (
          <>
            {" · "}
            {t("users.credit.expiresOn")}
            {": "}
            <Time at={granted.expires_at} />
          </>
        ) : null}
      </p>
    </>
  );
}
