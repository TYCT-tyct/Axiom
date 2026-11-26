import { type AdminSchemas, ApiError } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import {
  ActionButton,
  Card,
  DataState,
  DrawerFacts,
  EmptyState,
  Figure,
  Grid,
  PaceBar,
  SegmentedControl,
  Text,
  useFormat,
} from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useDuration } from "../durations";
import { Actions, State } from "../parts";
import { accountQuery } from "../queries";
import { SubscriptionPlanDialog } from "../subscription-plan-dialog";
import { subscriptionPlansQuery, subscriptionSummaryQuery } from "../subscription-queries";

type Plan = AdminSchemas["SubscriptionPlan"];
const newPlan = "new";

// The plans tab: one card per plan, with what it costs, what each window gives, how many accounts hold it and what share of
// all active subscriptions that is. The holders and shares are the summary's, so a session without sites.manage, or a node
// without the summary yet (501), sees the cards without them. A plan is made and edited by pricing.manage, against the
// revision it was read at (the existing dialog).
export function PlansTab() {
  const { t } = useTranslation();
  const session = useSession();
  const { include_inactive } = useSearch({ strict: false }) as { include_inactive?: boolean };
  const all = Boolean(include_inactive);
  const plans = useQuery(subscriptionPlansQuery(all));
  const summary = useQuery({ ...subscriptionSummaryQuery, enabled: can(session, "sites.manage") });
  const owner = useQuery(accountQuery(session.account));
  const manage = can(session, "pricing.manage");
  const [editing, setEditing] = useState<string>();
  const current = plans.data?.data.find((plan) => plan.id === editing);
  const currency = owner.data?.balance.balance.currency ?? current?.price.currency ?? "USD";
  const shares = new Map((summary.data?.plans ?? []).map((plan) => [plan.plan_id, plan]));
  if (plans.error instanceof ApiError && plans.error.status === 404) {
    return (
      <Card title={t("subscriptions.unavailableTitle")}>
        <Text tone="muted">{t("subscriptions.unavailableBody")}</Text>
      </Card>
    );
  }
  return (
    <>
      <PlanActions all={all} onNew={manage ? () => setEditing(newPlan) : undefined} />
      <DataState
        query={plans}
        isEmpty={(data) => data.data.length === 0}
        empty={<EmptyState title={t("subscriptions.empty.title")}>{t("subscriptions.empty.body")}</EmptyState>}
      >
        {(data) => (
          <Grid min="16rem">
            {data.data.map((plan) => (
              <PlanCard
                key={plan.id}
                plan={plan}
                share={shares.get(plan.id) ?? (summary.data ? { plan_id: plan.id, active: 0, share_bp: 0 } : undefined)}
                onEdit={manage ? () => setEditing(plan.id) : undefined}
              />
            ))}
          </Grid>
        )}
      </DataState>
      <PlanDialog editing={editing} current={current} currency={currency} onClose={() => setEditing(undefined)} />
    </>
  );
}

// Which plans are listed, and the plan to make.
function PlanActions({ all, onNew }: { all: boolean; onNew: (() => void) | undefined }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  return (
    <Actions>
      <SegmentedControl
        label={t("subscriptions.col.state")}
        value={all ? "all" : "active"}
        options={[
          { value: "active", label: t("subscriptions.activeOnly") },
          { value: "all", label: t("subscriptions.includeInactive") },
        ]}
        onChange={(value) =>
          void navigate({
            to: "/subscriptions/plans",
            search: { include_inactive: value === "all" ? true : undefined },
          })
        }
      />
      {onNew ? (
        <ActionButton variant="pri" icon="plus" onClick={onNew}>
          {t("subscriptions.new")}
        </ActionButton>
      ) : null}
    </Actions>
  );
}

// The dialog to make a plan, or to edit the plan being edited, which takes its revision from the plan as it was read.
function PlanDialog({
  editing,
  current,
  currency,
  onClose,
}: {
  editing: string | undefined;
  current: Plan | undefined;
  currency: string;
  onClose: () => void;
}) {
  if (editing === newPlan) return <SubscriptionPlanDialog currency={currency} onClose={onClose} />;
  return current ? (
    <SubscriptionPlanDialog key={current.id} plan={current} currency={currency} onClose={onClose} />
  ) : null;
}

function PlanCard({
  plan,
  share,
  onEdit,
}: {
  plan: Plan;
  share: AdminSchemas["SubscriptionPlanShare"] | undefined;
  onEdit: (() => void) | undefined;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const duration = useDuration();
  return (
    <Card
      title={plan.name}
      lede={t("subscriptions.plan.lasts", { span: duration(plan.term_seconds) })}
      actions={
        <State tone={plan.active ? "good" : "neutral"}>
          {plan.active ? t("subscriptions.active") : t("subscriptions.inactive")}
        </State>
      }
    >
      <Figure value={format.money(plan.price)} />
      <DrawerFacts
        rows={[
          [
            t("subscriptions.plan.quotaEvery", { span: duration(plan.window_seconds) }),
            format.money({ amount: plan.quota, currency: plan.price.currency }),
          ],
          ...(share
            ? [
                [
                  t("subscriptions.plan.holders"),
                  share.active > 0
                    ? t("subscriptions.plan.accounts", { count: share.active })
                    : t("subscriptions.plan.nobody"),
                ] as const,
              ]
            : []),
        ]}
      />
      {share ? (
        <>
          <PaceBar label={t("subscriptions.plan.share")} value={share.share_bp} max={10_000} />
          <Text size="sm" tone="muted" numeric>
            {t("subscriptions.plan.shareOf", { percent: format.number(share.share_bp / 100) })}
          </Text>
        </>
      ) : null}
      {onEdit ? (
        <ActionButton aria-label={t("subscriptions.editNamed", { name: plan.name })} onClick={onEdit}>
          {t("subscriptions.edit")}
        </ActionButton>
      ) : null}
    </Card>
  );
}
