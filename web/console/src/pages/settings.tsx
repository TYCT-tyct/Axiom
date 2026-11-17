import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { can, useErrors, useSession } from "@axiom/app";
import { DataState, FormError, Notice, PageHeader, Panel, SelectField, SubmitButton } from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { billingPreferenceQuery } from "../queries";

// Settings is where the account chooses how calls are paid for (docs/domains/billing.md「资金来源」). The site sets a
// default; the account may follow it or choose one of the four orders. It is also the way to the workspaces page, for
// whoever may read keys; the page checks the managing permission for each change itself.
export default function Settings() {
  const { t } = useTranslation();
  const preference = useQuery(billingPreferenceQuery);
  const session = useSession();
  const workspaces = can(session, "api_keys.view");
  return (
    <>
      <PageHeader title={t("nav.settings")} description={t("settings.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        <Panel title={t("settings.billing.title")}>
          <DataState query={preference}>{(data) => <PreferenceForm data={data} />}</DataState>
        </Panel>
        {workspaces ? (
          <Panel title={t("workspaces.settings.title")}>
            <div className="max-w-xl space-y-3 p-4">
              <p className="max-w-prose text-ui text-t3">{t("workspaces.settings.body")}</p>
              <Link to="/workspaces" className="text-ui text-accent underline-offset-4 hover:underline">
                {t("workspaces.settings.link")}
              </Link>
            </div>
          </Panel>
        ) : null}
      </div>
    </>
  );
}

const preferences = ["subscription_first", "wallet_first", "subscription_only", "wallet_only"] as const;

function SavedNotice({ onDismiss }: { onDismiss: () => void }) {
  const { t } = useTranslation();
  return (
    <Notice
      action={
        <button type="button" onClick={onDismiss}>
          {t("settings.dismiss")}
        </button>
      }
    >
      {t("settings.saved")}
    </Notice>
  );
}

function PreferenceForm({ data }: { data: Schemas["BillingPreference"] }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const client = useQueryClient();
  const [value, setValue] = useState<string>(data.source === "account" ? data.preference : "site");
  const [saved, setSaved] = useState(false);
  const [failure, setFailure] = useState<unknown>(null);
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        consoleApi.PUT("/api/console/v1/billing-preference", {
          body: { preference: value as Schemas["BillingPreferenceRequest"]["preference"] },
        }),
      ),
    onSuccess: (next) => {
      client.setQueryData(billingPreferenceQuery.queryKey, next);
      setValue(next.source === "account" ? next.preference : "site");
      setFailure(null);
      setSaved(true);
    },
    onError: (error) => {
      setFailure(error);
      setSaved(false);
    },
  });
  return (
    <form
      className="max-w-xl space-y-5 p-4"
      onSubmit={(event) => {
        event.preventDefault();
        save.mutate();
      }}
    >
      {saved ? <SavedNotice onDismiss={() => setSaved(false)} /> : null}
      <SelectField
        label={t("settings.billing.preference")}
        hint={t("settings.billing.hint", { fallback: t(`settings.billing.values.${data.site_default}`) })}
        name="preference"
        value={value}
        onChange={(event) => setValue(event.target.value)}
      >
        <option value="site">
          {t("settings.billing.site", { value: t(`settings.billing.values.${data.site_default}`) })}
        </option>
        {preferences.map((name) => (
          <option key={name} value={name}>
            {t(`settings.billing.values.${name}`)}
          </option>
        ))}
      </SelectField>
      <p className="max-w-prose text-ui text-t3">{t("settings.billing.fallback")}</p>
      {failure ? <FormError>{errors.message(failure)}</FormError> : null}
      <SubmitButton pending={save.isPending}>{t("settings.save")}</SubmitButton>
    </form>
  );
}
