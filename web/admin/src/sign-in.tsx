import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { ActionButton, Card, CheckField, Cluster, DataState, Field, SettingRow, Tag } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";
import { Facts } from "./parts";
import { siteSignInQuery } from "./queries";

type View = AdminSchemas["SiteSignIn"];
type Provider = AdminSchemas["SiteOAuthProvider"];
type ProviderInput = { client_id: string; client_secret: string };

// SiteSignIn is where a site's owner chooses the domain third-party callbacks and passkeys live on, turns passkeys
// on, and stores each provider's application. The secret is sealed on arrival and never shown again.
export function SiteSignIn({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const query = useQuery(siteSignInQuery(siteId));
  return <DataState query={query}>{(view) => <SignInCard siteId={siteId} view={view} />}</DataState>;
}

function SignInCard({ siteId, view }: { siteId: AdminSchemas["SiteId"]; view: View }) {
  const { t } = useTranslation();
  const [edit, setEdit] = useState(false);
  const [provider, setProvider] = useState<{ name: Provider; remove: boolean } | null>(null);
  const rows: [string, ReactNode][] = [
    [t("settings.signIn.loginDomain"), view.login_domain ?? t("settings.signIn.domainNone")],
    [t("settings.signIn.passkeys"), view.passkeys ? t("settings.signIn.on") : t("settings.signIn.off")],
  ];
  return (
    <>
      <Facts
        title={t("settings.signIn.title")}
        rows={rows}
        actions={
          <ActionButton onClick={() => setEdit(true)} icon="pencil">
            {t("settings.signIn.edit")}
          </ActionButton>
        }
      />
      <Card layout="rows">
        {view.providers.map((entry) => (
          <ProviderLine
            key={entry.provider}
            provider={entry}
            onEdit={() => setProvider({ name: entry, remove: false })}
            onRemove={() => setProvider({ name: entry, remove: true })}
          />
        ))}
      </Card>
      {edit ? <SignInDialog siteId={siteId} view={view} onClose={() => setEdit(false)} /> : null}
      {provider && !provider.remove ? (
        <ProviderDialog siteId={siteId} provider={provider.name} onClose={() => setProvider(null)} />
      ) : null}
      {provider?.remove ? (
        <RemoveProviderDialog siteId={siteId} provider={provider.name} onClose={() => setProvider(null)} />
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

function SignInDialog({ siteId, view, onClose }: { siteId: AdminSchemas["SiteId"]; view: View; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`site-sign-in:${siteId}`}
      title={t("settings.signIn.edit")}
      intro={<p>{t("settings.signIn.intro")}</p>}
      action={t("settings.signIn.save")}
      fields={(error) => <SignInFields view={view} error={error} />}
      read={(values) => ({
        login_domain: (values.login_domain ?? "").trim().toLowerCase(),
        passkeys: values.passkeys === "true",
      })}
      send={(body, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/sign-in", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { ...body, reason },
          }),
        )
      }
      done={() => <p>{t("settings.signIn.saved")}</p>}
      onClose={onClose}
    />
  );
}

function SignInFields({ view, error }: { view: View; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <Field
        label={t("settings.signIn.loginDomain")}
        hint={t("settings.signIn.domainHint")}
        name="login_domain"
        defaultValue={view.login_domain ?? ""}
        error={errors.field(error, "login_domain")}
      />
      <CheckField
        label={t("settings.signIn.passkeys")}
        hint={t("settings.signIn.passkeysHint")}
        name="passkeys"
        value="true"
        defaultChecked={view.passkeys}
      />
    </>
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
