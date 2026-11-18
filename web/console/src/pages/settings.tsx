import { can, useErrors, useSession } from "@axiom/app";
import { DataState, FormError, Notice, PageHeader, Panel, SelectField, SubmitButton } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import {
  type BillingPreference,
  type BillingPreferenceChoice,
  billingPreferenceOptions,
  useBillingPreferenceForm,
} from "../billing-preference";
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

function PreferenceForm({ data }: { data: BillingPreference }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const form = useBillingPreferenceForm(data);
  const options = billingPreferenceOptions(
    data.site_default,
    (key) => t(`settings.billing.values.${key}`),
    (defaultLabel) => t("settings.billing.site", { value: defaultLabel }),
  );
  return (
    <form
      className="max-w-xl space-y-5 p-4"
      onSubmit={(event) => {
        event.preventDefault();
        form.submit();
      }}
    >
      {form.saved ? <SavedNotice onDismiss={form.dismissSaved} /> : null}
      <SelectField
        label={t("settings.billing.preference")}
        hint={t("settings.billing.hint", { fallback: t(`settings.billing.values.${data.site_default}`) })}
        name="preference"
        value={form.choice}
        onChange={(event) => form.selectChoice(event.target.value as BillingPreferenceChoice)}
      >
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </SelectField>
      <p className="max-w-prose text-ui text-t3">{t("settings.billing.fallback")}</p>
      {form.error ? <FormError>{errors.message(form.error)}</FormError> : null}
      <SubmitButton pending={form.pending}>{t("settings.save")}</SubmitButton>
    </form>
  );
}
