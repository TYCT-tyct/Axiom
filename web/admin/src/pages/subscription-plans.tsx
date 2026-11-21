import { type AdminSchemas, ApiError } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import {
  ActionButton,
  Card,
  DataState,
  EmptyState,
  Id,
  PageHead,
  SegmentedControl,
  Stack,
  Table,
  Td,
  Text,
  useFormat,
} from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useDuration } from "../durations";
import { Actions, Head, PageBody, State, SubLine } from "../parts";
import { accountQuery } from "../queries";
import { SubscriptionPlanDialog } from "../subscription-plan-dialog";
import { subscriptionPlansQuery } from "../subscription-queries";

type Plan = AdminSchemas["SubscriptionPlan"];
const newPlan = "new";

// SubscriptionPlansPage manages the site's subscription plan catalog: active plans by default, or all plans including
// inactive ones, with creation and revision-checked updates.
export default function SubscriptionPlansPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { include_inactive } = useSearch({ from: "/app/subscription-plans" });
  const includeInactive = Boolean(include_inactive);
  const plans = useQuery(subscriptionPlansQuery(includeInactive));
  const owner = useQuery(accountQuery(useSession().account));
  const manage = can(useSession(), "pricing.manage");
  const [editing, setEditing] = useState<string | null>(null);
  const current = plans.data?.data.find((plan) => plan.id === editing);
  const currency = owner.data?.balance.balance.currency ?? current?.price.currency ?? "USD";

  if (plans.error instanceof ApiError && plans.error.status === 404) {
    return <PlansUnavailable />;
  }

  return (
    <>
      <PageHead title={t("subscriptions.title")} lede={t("subscriptions.description")} />
      <PageBody>
        {manage ? (
          <Actions>
            <ActionButton variant="pri" icon="plus" onClick={() => setEditing(newPlan)}>
              {t("subscriptions.new")}
            </ActionButton>
          </Actions>
        ) : null}
        <Card
          layout="flush"
          title={t("subscriptions.listTitle")}
          actions={
            <SegmentedControl
              label={t("subscriptions.col.state")}
              value={includeInactive ? "all" : "active"}
              options={[
                { value: "active", label: t("subscriptions.activeOnly") },
                { value: "all", label: t("subscriptions.includeInactive") },
              ]}
              onChange={(value) =>
                navigate({
                  to: "/subscription-plans",
                  search: { include_inactive: value === "all" ? true : undefined },
                })
              }
            />
          }
        >
          <DataState
            query={plans}
            isEmpty={(data) => data.data.length === 0}
            empty={<EmptyState title={t("subscriptions.empty.title")}>{t("subscriptions.empty.body")}</EmptyState>}
          >
            {(data) => <PlansTable plans={data.data} onEdit={manage ? (plan) => setEditing(plan.id) : undefined} />}
          </DataState>
        </Card>
      </PageBody>
      <OpenPlanDialog editing={editing} current={current} currency={currency} onClose={() => setEditing(null)} />
    </>
  );
}

function PlansUnavailable() {
  const { t } = useTranslation();
  return (
    <>
      <PageHead title={t("subscriptions.title")} lede={t("subscriptions.description")} />
      <PageBody>
        <Card title={t("subscriptions.unavailableTitle")}>
          <Text tone="muted">{t("subscriptions.unavailableBody")}</Text>
        </Card>
      </PageBody>
    </>
  );
}

function OpenPlanDialog({
  editing,
  current,
  currency,
  onClose,
}: {
  editing: string | null;
  current: Plan | undefined;
  currency: string;
  onClose: () => void;
}) {
  if (editing === newPlan) {
    return <SubscriptionPlanDialog currency={currency} onClose={onClose} />;
  }
  if (editing && current) {
    return <SubscriptionPlanDialog key={current.id} plan={current} currency={currency} onClose={onClose} />;
  }
  return null;
}

function PlansTable({ plans, onEdit }: { plans: Plan[]; onEdit: ((plan: Plan) => void) | undefined }) {
  const { t } = useTranslation();
  const format = useFormat();
  const duration = useDuration();
  return (
    <Table label={t("subscriptions.listTitle")}>
      <Head
        cols={[
          [t("subscriptions.col.name"), { main: true }],
          [t("subscriptions.col.price"), { align: "end" }],
          [t("subscriptions.col.quota"), { align: "end" }],
          t("subscriptions.col.window"),
          t("subscriptions.col.term"),
          t("subscriptions.col.state"),
          t("subscriptions.col.updated"),
        ]}
        action={onEdit !== undefined}
      />
      <tbody>
        {plans.map((plan) => (
          <tr key={plan.id}>
            <Td main>
              <Stack gap={4}>
                <Text as="span">{plan.name}</Text>
                <Id value={plan.id} />
              </Stack>
            </Td>
            <Td align="end">{format.money(plan.price)}</Td>
            <Td align="end">{format.money({ amount: plan.quota, currency: plan.price.currency })}</Td>
            <Td shrink>{duration(plan.window_seconds)}</Td>
            <Td shrink>{duration(plan.term_seconds)}</Td>
            <Td>
              <State tone={plan.active ? "good" : "neutral"}>
                {plan.active ? t("subscriptions.active") : t("subscriptions.inactive")}
              </State>
            </Td>
            <Td shrink>
              <span title={format.dateTime(new Date(plan.updated_at))}>
                {format.dayFull(new Date(plan.updated_at))}
              </span>
              <SubLine>{`${t("subscriptions.col.rev")} ${plan.rev}`}</SubLine>
            </Td>
            {onEdit ? (
              <Td actions>
                <ActionButton
                  size="sm"
                  aria-label={t("subscriptions.editNamed", { name: plan.name })}
                  onClick={() => onEdit(plan)}
                >
                  {t("subscriptions.edit")}
                </ActionButton>
              </Td>
            ) : null}
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

export { default as messages } from "../locales/subscriptions";
