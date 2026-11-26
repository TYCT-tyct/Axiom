import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Field, Stack, Text, TextArea, useFormat } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "./change";
import { readBatch } from "./redeem";

type RedeemCode = AdminSchemas["RedeemCode"];

// DisableDialog stops one code; the key makes a retry the same disable, and the reason goes to the audit log. Nothing
// enables a disabled code again, so the change has no undo and says that it is final.
export function DisableDialog({ code, onClose }: { code: RedeemCode; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`disable-redeem-code:${code.id}`}
      title={t("redeem.disableTitle", { hint: code.hint })}
      intro={<p>{t("redeem.disableIntro")}</p>}
      warn={t("redeem.disableFinal")}
      action={t("redeem.disable")}
      danger
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/payments/redeem-codes/{code_id}/disable", {
            params: { path: { code_id: code.id }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={() => <Text tone="muted">{t("redeem.disabled")}</Text>}
      onClose={onClose}
    />
  );
}

// GenerateDialog makes one batch. The answer carries the codes; they are not read back by any later request, so the
// result is the only place they are shown, and it has no toast and no undo.
export function GenerateDialog({
  siteId,
  currency,
  onClose,
}: {
  siteId: AdminSchemas["SiteId"];
  currency: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const words = {
    amountInvalid: t("redeem.amountInvalid"),
    countInvalid: t("redeem.countInvalid"),
    noteInvalid: t("redeem.noteInvalid"),
    expiresInvalid: t("redeem.expiresInvalid"),
  };
  return (
    <ChangeDialog
      operation={`create-redeem-codes:${siteId}`}
      title={t("redeem.generateTitle")}
      intro={<p>{t("redeem.generateIntro")}</p>}
      action={t("redeem.generate")}
      fields={(error) => <BatchFields currency={currency} error={error} />}
      read={(values) => readBatch(values, siteId, currency, words)}
      send={(batch, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/payments/redeem-codes", {
            params: { header: { "Idempotency-Key": key } },
            body: { ...batch, reason },
          }),
        )
      }
      done={(list) => <Issued list={list} />}
      onClose={onClose}
    />
  );
}

function BatchFields({ currency, error }: { currency: string; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <Field
        label={t("redeem.amount", { currency })}
        hint={t("redeem.amountHint")}
        name="amount"
        inputMode="decimal"
        autoComplete="off"
        error={errors.field(error, "amount")}
      />
      <Field
        label={t("redeem.count")}
        hint={t("redeem.countHint")}
        name="count"
        inputMode="numeric"
        autoComplete="off"
        error={errors.field(error, "count")}
      />
      <Field
        label={t("redeem.note")}
        hint={t("redeem.noteHint")}
        name="note"
        maxLength={200}
        autoComplete="off"
        error={errors.field(error, "note")}
      />
      <Field
        label={t("redeem.expires")}
        hint={t("redeem.expiresHint")}
        name="expires_at"
        type="datetime-local"
        error={errors.field(error, "expires_at")}
      />
    </>
  );
}

// Issued shows a batch's codes the one time they are known, as text a person can select and keep.
function Issued({ list }: { list: AdminSchemas["RedeemCodeList"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  const codes = (list.data ?? []).flatMap((code) => (code.code ? [code.code] : []));
  return (
    <Stack>
      {codes.length === 0 ? (
        <Text tone="muted">{t("redeem.issuedGone")}</Text>
      ) : (
        <Text tone="muted">{t("redeem.issuedIntro", { count: codes.length })}</Text>
      )}
      {codes.length > 0 ? (
        <TextArea
          label={t("redeem.issuedLabel")}
          hint={t("redeem.issuedHint")}
          readOnly
          rows={Math.min(codes.length + 1, 12)}
          value={codes.join("\n")}
          onFocus={(event) => event.currentTarget.select()}
        />
      ) : null}
      {list.data?.[0] ? (
        <Text tone="muted">{t("redeem.issuedAmount", { amount: format.money(list.data[0].amount) })}</Text>
      ) : null}
    </Stack>
  );
}
