import type { AdminSchemas } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { TextAreaField, TextField } from "@axiom/ui";
import { useTranslation } from "react-i18next";

type Merchant = AdminSchemas["Merchant"];

// MerchantSecrets shows the settings JSON and the write-only secrets of the merchant kind being edited.
export function MerchantSecrets({
  merchant,
  kind,
  error,
}: {
  merchant: Merchant | undefined;
  kind: string;
  error: unknown;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const stripe = kind === "stripe";
  const defaults =
    kind === "alipay"
      ? { app_id: "", seller_id: "", gateway: "https://openapi.alipay.com/gateway.do" }
      : kind === "wechatpay"
        ? { app_id: "", mch_id: "", serial_no: "", gateway: "https://api.mch.weixin.qq.com" }
        : stripe
          ? {}
          : { gateway: "https://", pid: "", mode: "redirect" };
  return (
    <>
      <TextAreaField
        key={kind}
        label={t("settings.payments.settings")}
        hint={stripe ? t("settings.payments.stripeSettingsHint") : t("settings.payments.settingsHint")}
        name="settings"
        defaultValue={JSON.stringify(merchant?.settings ?? defaults, null, 2)}
        error={errors.field(error, "merchant/settings")}
      />
      {stripe ? (
        <StripeSecrets merchant={merchant} error={error} />
      ) : kind === "alipay" ? (
        <AlipaySecrets merchant={merchant} error={error} />
      ) : kind === "wechatpay" ? (
        <WechatSecrets merchant={merchant} error={error} />
      ) : (
        <EasyPaySecrets merchant={merchant} error={error} />
      )}
    </>
  );
}

function StripeSecrets({ merchant, error }: { merchant: Merchant | undefined; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <TextField
        label={t("settings.payments.stripeSecretKey")}
        hint={merchant ? t("settings.payments.stripeSecretKeep") : t("settings.payments.stripeSecretNew")}
        name="secret_key"
        type="password"
        autoComplete="new-password"
        error={errors.field(error, "merchant/secrets")}
      />
      <TextField
        label={t("settings.payments.stripeWebhookSecret")}
        hint={merchant ? t("settings.payments.stripeWebhookKeep") : t("settings.payments.stripeWebhookNew")}
        name="webhook_secret"
        type="password"
        autoComplete="new-password"
        error={errors.field(error, "merchant/secrets")}
      />
      <TextField
        label={t("settings.payments.stripeOldWebhookSecret")}
        hint={t("settings.payments.stripeOldWebhookHint")}
        name="old_webhook_secret"
        type="password"
        autoComplete="new-password"
      />
    </>
  );
}

function AlipaySecrets({ merchant, error }: { merchant: Merchant | undefined; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <TextAreaField
        label={t("settings.payments.privateKey")}
        hint={merchant ? t("settings.payments.privateKeyKeep") : t("settings.payments.privateKeyNew")}
        name="private_key"
        autoComplete="off"
        spellCheck={false}
        error={errors.field(error, "merchant/secrets")}
      />
      <TextAreaField
        label={t("settings.payments.publicKey")}
        hint={t("settings.payments.publicKeyHint")}
        name="public_key"
        autoComplete="off"
        spellCheck={false}
      />
      <TextAreaField
        label={t("settings.payments.oldPublicKey")}
        hint={t("settings.payments.oldPublicKeyHint")}
        name="old_public_key"
        autoComplete="off"
        spellCheck={false}
      />
    </>
  );
}

function WechatSecrets({ merchant, error }: { merchant: Merchant | undefined; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <TextAreaField
        label={t("settings.payments.wechatPrivateKey")}
        hint={merchant ? t("settings.payments.wechatPrivateKeyKeep") : t("settings.payments.wechatPrivateKeyNew")}
        name="private_key"
        autoComplete="off"
        spellCheck={false}
        error={errors.field(error, "merchant/secrets")}
      />
      <TextField
        label={t("settings.payments.wechatAPIv3Key")}
        hint={t("settings.payments.wechatAPIv3KeyHint")}
        name="api_v3_key"
        type="password"
        autoComplete="new-password"
        error={errors.field(error, "merchant/secrets")}
      />
      <TextAreaField
        label={t("settings.payments.wechatPlatformCertificate")}
        hint={t("settings.payments.wechatPlatformCertificateHint")}
        name="platform_certificate"
        autoComplete="off"
        spellCheck={false}
      />
      <TextAreaField
        label={t("settings.payments.wechatPlatformPublicKey")}
        hint={t("settings.payments.wechatPlatformPublicKeyHint")}
        name="platform_public_key"
        autoComplete="off"
        spellCheck={false}
      />
      <TextAreaField
        label={t("settings.payments.wechatOldPlatformCertificate")}
        hint={t("settings.payments.wechatOldPlatformCertificateHint")}
        name="old_platform_certificate"
        autoComplete="off"
        spellCheck={false}
      />
      <TextAreaField
        label={t("settings.payments.wechatOldPlatformPublicKey")}
        hint={t("settings.payments.wechatOldPlatformPublicKeyHint")}
        name="old_platform_public_key"
        autoComplete="off"
        spellCheck={false}
      />
    </>
  );
}

function EasyPaySecrets({ merchant, error }: { merchant: Merchant | undefined; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <TextField
        label={t("settings.payments.key")}
        hint={merchant ? t("settings.payments.keyKeep") : t("settings.payments.keyNew")}
        name="key"
        type="password"
        autoComplete="new-password"
        error={errors.field(error, "merchant/secrets")}
      />
      <TextField
        label={t("settings.payments.oldKey")}
        hint={t("settings.payments.oldKeyHint")}
        name="old_key"
        type="password"
        autoComplete="new-password"
      />
    </>
  );
}
