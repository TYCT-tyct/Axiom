import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { ActionButton, Cluster, Field, SettingRow, Tag } from "@axiom/ui";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";
import { SettingsCard } from "./setting-rows";

type Provider = AdminSchemas["SiteOAuthProvider"];
type ProviderInput = { client_id: string; client_secret: string };

// Providers is the third-party sign-in of a site: each provider's application, whose secret is sealed on arrival and
// never shown again, so setting it and taking it off are dialogs of their own and not drafts.
export function Providers({ siteId, providers }: { siteId: AdminSchemas["SiteId"]; providers: Provider[] }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState<{ name: Provider; remove: boolean } | null>(null);
  return (
    <>
      <SettingsCard
        scope="site"
        title={t("settings.pages.signIn.providers")}
        lede={t("settings.pages.signIn.providersLede")}
      >
        {providers.map((entry) => (
          <ProviderLine
            key={entry.provider}
            provider={entry}
            onEdit={() => setOpen({ name: entry, remove: false })}
            onRemove={() => setOpen({ name: entry, remove: true })}
          />
        ))}
      </SettingsCard>
      {open && !open.remove ? (
        <ProviderDialog siteId={siteId} provider={open.name} onClose={() => setOpen(null)} />
      ) : null}
      {open?.remove ? (
        <RemoveProviderDialog siteId={siteId} provider={open.name} onClose={() => setOpen(null)} />
      ) : null}
    </>
  );
}

function ProviderLine({
  provider,
  onEdit,
  onRemove,
}: {
  provider: Provider;
  onEdit: () => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  return (
    <SettingRow
      title={t(`settings.signIn.providers.${provider.provider}`)}
      detail={
        <Tag tone={provider.configured ? "ok" : "neutral"}>
          {provider.configured ? provider.client_id : t("settings.signIn.providerOff")}
        </Tag>
      }
    >
      <Cluster>
        <ActionButton size="sm" onClick={onEdit}>
          {provider.configured ? t("settings.signIn.providerEdit") : t("settings.signIn.providerConfigure")}
        </ActionButton>
        {provider.configured ? (
          <ActionButton size="sm" onClick={onRemove}>
            {t("settings.signIn.providerRemove")}
          </ActionButton>
        ) : null}
      </Cluster>
    </SettingRow>
  );
}

function ProviderDialog({
  siteId,
  provider,
  onClose,
}: {
  siteId: AdminSchemas["SiteId"];
  provider: Provider;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const words = [t("settings.signIn.missingClientId"), t("settings.signIn.missingClientSecret")];
  return (
    <ChangeDialog
      operation={`site-oauth-provider:${siteId}:${provider.provider}`}
      title={t("settings.signIn.providerDialog", { provider: t(`settings.signIn.providers.${provider.provider}`) })}
      intro={<p>{t("settings.signIn.providerIntro")}</p>}
      action={t("settings.signIn.providerSave")}
      fields={(error) => <ProviderFields provider={provider} error={error} />}
      read={(values): ProviderInput | Problem => {
        const clientID = (values.client_id ?? "").trim();
        const clientSecret = values.client_secret ?? "";
        if (clientID === "") return { problem: words[0] ?? "" };
        if (clientSecret === "") return { problem: words[1] ?? "" };
        return { client_id: clientID, client_secret: clientSecret };
      }}
      send={(body, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/sign-in/providers/{provider}", {
            params: { path: { site_id: siteId, provider: provider.provider }, header: { "Idempotency-Key": key } },
            body: { ...body, reason },
          }),
        )
      }
      done={() => <p>{t("settings.signIn.providerSaved")}</p>}
      onClose={onClose}
    />
  );
}

function ProviderFields({ provider, error }: { provider: Provider; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <Field
        label={t("settings.signIn.clientId")}
        name="client_id"
        autoComplete="off"
        defaultValue={provider.client_id ?? ""}
        error={errors.field(error, "client_id")}
      />
      <Field
        label={t("settings.signIn.clientSecret")}
        hint={t("settings.signIn.secretHint")}
        name="client_secret"
        type="password"
        autoComplete="new-password"
        error={errors.field(error, "client_secret")}
      />
    </>
  );
}

function RemoveProviderDialog({
  siteId,
  provider,
  onClose,
}: {
  siteId: AdminSchemas["SiteId"];
  provider: Provider;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`site-oauth-provider-remove:${siteId}:${provider.provider}`}
      title={t("settings.signIn.providerRemove")}
      intro={<p>{t("settings.signIn.removeIntro")}</p>}
      diff={[
        {
          label: t(`settings.signIn.providers.${provider.provider}`),
          before: t("settings.signIn.providerOn"),
          after: t("settings.signIn.providerOff"),
        },
      ]}
      warn={t("settings.signIn.removeWarn", { provider: t(`settings.signIn.providers.${provider.provider}`) })}
      action={t("settings.signIn.providerRemove")}
      danger
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/sites/{site_id}/sign-in/providers/{provider}/delete", {
            params: { path: { site_id: siteId, provider: provider.provider }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={() => <p>{t("settings.signIn.removed")}</p>}
      onClose={onClose}
    />
  );
}
