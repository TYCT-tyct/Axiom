import { ApiError, consoleApi, unwrap } from "@axiom/api";
import { useErrors } from "@axiom/app";
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
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { balanceQuery, billingPreferenceQuery, siteQuery } from "../queries";
import { type AccountSubscription, accountSubscriptionsQuery, type BillingPreferenceName } from "./bill-data";

const PREFERENCE_KEYS: readonly BillingPreferenceName[] = [
  "subscription_first",
  "wallet_first",
  "subscription_only",
  "wallet_only",
];

type PreferenceChoice = BillingPreferenceName | "site";
type PrefData = { preference: BillingPreferenceName; source: "account" | "site"; site_default: BillingPreferenceName };

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

  const currency = site.data?.currency ?? balance.data?.available.currency ?? "USD";
  return (
    <DataState query={subscriptions}>
      {({ data }) =>
        data.length === 0 ? (
          <Card title={t("statement.bill.plans.title")} lede={t("statement.bill.plans.subtitle")}>
            <EmptyState title={t("statement.bill.plans.empty")}>{t("statement.bill.plans.emptyBody")}</EmptyState>
          </Card>
        ) : (
          <SubscriptionsTable subs={data} currency={currency} />
        )
      }
    </DataState>
  );
}

function useChargeOrderSave(onSaved: () => void) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (preference: PreferenceChoice) =>
      unwrap(consoleApi.PUT("/api/console/v1/billing-preference", { body: { preference } })),
    onSuccess: (updated) => {
      queryClient.setQueryData(billingPreferenceQuery.queryKey, updated);
      onSaved();
    },
  });
}

function chargeOrderOptions(
  siteDefault: BillingPreferenceName,
  t: {
    (key: `settings.billing.values.${BillingPreferenceName}`): string;
    (key: "settings.billing.site", opts: { value: string }): string;
  },
) {
  const labels: Record<BillingPreferenceName, string> = {
    subscription_first: t("settings.billing.values.subscription_first"),
    wallet_first: t("settings.billing.values.wallet_first"),
    subscription_only: t("settings.billing.values.subscription_only"),
    wallet_only: t("settings.billing.values.wallet_only"),
  };
  return [
    { value: "site", label: t("settings.billing.site", { value: labels[siteDefault] }) },
    ...PREFERENCE_KEYS.map((key) => ({ value: key, label: labels[key] })),
  ];
}

function ChargeOrderForm({ pref }: { pref: PrefData }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [choice, setChoice] = useState<PreferenceChoice>(() => (pref.source === "site" ? "site" : pref.preference));
  const [savedNotice, setSavedNotice] = useState(false);

  useEffect(() => {
    setChoice(pref.source === "site" ? "site" : pref.preference);
  }, [pref.source, pref.preference]);

  const save = useChargeOrderSave(() => setSavedNotice(true));
  const options = chargeOrderOptions(pref.site_default, t);
  const sourceNote = t(
    pref.source === "account" ? "statement.bill.plans.preferenceAccount" : "statement.bill.plans.preferenceSite",
  );

  return (
    <Card title={t("settings.billing.title")} lede={t("settings.description")} layout="rows">
      <form
        onSubmit={(event) => {
          event.preventDefault();
          setSavedNotice(false);
          save.mutate(choice);
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
              value={choice}
              options={options}
              onChange={(event) => {
                setSavedNotice(false);
                setChoice(event.target.value as PreferenceChoice);
              }}
            />
            <SubmitButton pending={save.isPending}>{t("settings.save")}</SubmitButton>
          </Cluster>
        </SettingRow>
        {save.error ? <FormError>{errors.message(save.error)}</FormError> : null}
        {savedNotice ? <Note title={t("settings.saved")} /> : null}
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
      <Card title={t("settings.billing.title")} lede={t("settings.description")}>
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
