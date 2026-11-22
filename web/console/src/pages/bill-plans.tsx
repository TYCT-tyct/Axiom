import { ApiError } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { siteQuery } from "@axiom/login";
import {
  Card,
  Cluster,
  DataState,
  DataTable,
  EmptyState,
  FormError,
  Note,
  Select,
  SettingRow,
  Stack,
  SubmitButton,
  Tag,
  useFormat,
} from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import {
  type BillingPreference,
  type BillingPreferenceChoice,
  billingPreferenceOptions,
  useBillingPreferenceForm,
} from "../billing-preference";
import { balanceQuery, billingPreferenceQuery } from "../queries";
import { type AccountSubscription, accountSubscriptionsQuery } from "./bill-data";

function PlanNameCell({ sub }: { sub: AccountSubscription }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Stack gap={4}>
      <Cluster gap={8}>
        <strong>{sub.plan_name}</strong>
        <Tag tone={sub.state === "active" ? "ok" : "neutral"}>
          {t(sub.state === "active" ? "statement.bill.plans.state.active" : "statement.bill.plans.state.expired")}
        </Tag>
      </Cluster>
      <span>{`${format.dateTimeMinute(new Date(sub.starts_at))} – ${format.dateTimeMinute(new Date(sub.ends_at))}`}</span>
    </Stack>
  );
}

function SubscriptionsTable({ subs, currency }: { subs: readonly AccountSubscription[]; currency: string }) {
  const { t } = useTranslation();
  const format = useFormat();
  const columns = [
    {
      key: "plan",
      header: t("statement.bill.plans.col.plan"),
      cell: (sub: AccountSubscription) => <PlanNameCell sub={sub} />,
    },
    {
      key: "window",
      header: t("statement.bill.plans.col.window"),
      cell: (sub: AccountSubscription) =>
        t("statement.bill.plans.window", {
          seq: sub.current_window.seq,
          since: format.dateTimeMinute(new Date(sub.current_window.starts_at)),
          until: format.dateTimeMinute(new Date(sub.current_window.ends_at)),
        }),
    },
    {
      key: "usage",
      header: t("statement.bill.plans.col.usage"),
      cell: (sub: AccountSubscription) =>
        t("statement.bill.plans.usageDetail", {
          used: format.money({ amount: sub.current_window.used, currency }),
          reserved: format.money({ amount: sub.current_window.reserved, currency }),
          quota: format.money({ amount: sub.current_window.quota, currency }),
        }),
    },
    {
      key: "remaining",
      header: t("statement.bill.plans.col.remaining"),
      align: "end" as const,
      cell: (sub: AccountSubscription) => (
        <strong>{format.money({ amount: sub.current_window.remaining, currency })}</strong>
      ),
    },
  ];
  return (
    <Card
      title={t("statement.bill.plans.title")}
      lede={t("statement.bill.plans.currencyNote", { currency })}
      layout="flush"
    >
      <DataTable<AccountSubscription>
        label={t("statement.bill.plans.title")}
        rows={subs}
        rowKey={(sub) => sub.id}
        columns={columns}
      />
    </Card>
  );
}

function SubscriptionsSection() {
  const { t } = useTranslation();
  const site = useQuery(siteQuery);
  const balance = useQuery(balanceQuery);
  const subscriptions = useQuery({
    ...accountSubscriptionsQuery,
    retry: (count, error) => !(error instanceof ApiError && error.status === 404) && count < 2,
  });

  if (subscriptions.error instanceof ApiError && subscriptions.error.status === 404) {
    return (
      <Card title={t("statement.bill.plans.title")} lede={t("statement.bill.plans.subtitle")}>
        <Note title={t("statement.bill.plans.unavailable")} />
      </Card>
    );
  }

  const currency = site.data?.currency ?? balance.data?.balance.currency;
  const currencySourceQuery = !site.data && site.isPending ? site : !balance.data && balance.isPending ? balance : site;
  return (
    <DataState query={subscriptions}>
      {({ data }) => {
        if (data.length === 0) {
          return (
            <Card title={t("statement.bill.plans.title")} lede={t("statement.bill.plans.subtitle")}>
              <EmptyState title={t("statement.bill.plans.empty")}>{t("statement.bill.plans.emptyBody")}</EmptyState>
            </Card>
          );
        }
        if (!currency) {
          return <DataState query={currencySourceQuery}>{() => null}</DataState>;
        }
        return <SubscriptionsTable subs={data} currency={currency} />;
      }}
    </DataState>
  );
}

function ChargeOrderForm({ pref }: { pref: BillingPreference }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const form = useBillingPreferenceForm(pref);
  const options = billingPreferenceOptions(
    pref.site_default,
    (key) => t(`settings.billing.values.${key}`),
    (defaultLabel) => t("settings.billing.site", { value: defaultLabel }),
  );
  const sourceNote = t(
    pref.source === "account" ? "statement.bill.plans.preferenceAccount" : "statement.bill.plans.preferenceSite",
  );

  return (
    <Card
      title={t("settings.billing.title")}
      lede={t("settings.billing.hint", { fallback: t(`settings.billing.values.${pref.site_default}`) })}
      layout="rows"
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          form.submit();
        }}
      >
        <SettingRow
          title={t("settings.billing.preference")}
          detail={
            <Stack gap={4}>
              <span>{sourceNote}</span>
              <span>{t("settings.billing.fallback")}</span>
            </Stack>
          }
        >
          <Cluster gap={8}>
            <Select
              label={t("settings.billing.preference")}
              value={form.choice}
              options={options}
              onChange={(event) => form.selectChoice(event.target.value as BillingPreferenceChoice)}
            />
            <SubmitButton pending={form.pending}>{t("settings.save")}</SubmitButton>
          </Cluster>
        </SettingRow>
        {form.error ? <FormError>{errors.message(form.error)}</FormError> : null}
        {form.saved ? <Note title={t("settings.saved")} /> : null}
      </form>
    </Card>
  );
}

function ChargeOrderSection() {
  const { t } = useTranslation();
  const query = useQuery({
    ...billingPreferenceQuery,
    retry: (count, error) => !(error instanceof ApiError && error.status === 404) && count < 2,
  });

  if (query.error instanceof ApiError && query.error.status === 404) {
    return (
      <Card title={t("settings.billing.title")}>
        <Note title={t("statement.bill.plans.preferenceUnavailable")} />
      </Card>
    );
  }

  return <DataState query={query}>{(pref) => <ChargeOrderForm pref={pref} />}</DataState>;
}

export function BillPlansPanel() {
  return (
    <Stack gap={16}>
      <SubscriptionsSection />
      <ChargeOrderSection />
    </Stack>
  );
}
