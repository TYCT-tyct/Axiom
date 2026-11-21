import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { can, useErrors, useSession } from "@axiom/app";
import { Cluster, CopyButton, Field, Mono, parseMoney, Select, Text, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";
import { paymentCurrencies } from "./credit-read";
import { useDuration } from "./durations";
import { Time } from "./parts";
import { subscriptionPlansQuery } from "./subscription-queries";

type Account = AdminSchemas["AccountDetail"];
type GrantInput = Omit<AdminSchemas["SubscriptionGrantInput"], "reason">;

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

type GrantWords = { planInvalid: string; paidInvalid: string; channelInvalid: string };

function readGrant(values: Record<string, string>, currency: string, words: GrantWords): GrantInput | Problem {
  const plan_id = (values.plan_id ?? "").trim().toLowerCase();
  if (!uuidPattern.test(plan_id)) {
    return { problem: words.planInvalid };
  }
  if (values.source !== "offline_payment") {
    return { plan_id, source: "gift" };
  }
  const amount = parseMoney(values.amount ?? "");
  if (amount === null || /^0+$/.test(amount)) {
    return { problem: words.paidInvalid };
  }
  const channel = (values.channel ?? "").trim();
  if (!channel || channel.length > 200) {
    return { problem: words.channelInvalid };
  }
  return {
    plan_id,
    source: "offline_payment",
    payment: { amount: { amount, currency: values.currency ?? currency }, channel },
  };
}

export function GrantSubscriptionDialog({ account, onClose }: { account: Account; onClose: () => void }) {
  const { t } = useTranslation();
  const canReadPlans = can(useSession(), "pricing.view");
  const plansQuery = useQuery({ ...subscriptionPlansQuery(false), enabled: canReadPlans });
  const plans = plansQuery.data?.data ?? [];
  const currency = account.balance.balance.currency;
  const words: GrantWords = {
    planInvalid: t("subscriptions.grant.planInvalid"),
    paidInvalid: t("subscriptions.grant.paidInvalid"),
    channelInvalid: t("subscriptions.grant.channelInvalid"),
  };
  return (
    <ChangeDialog
      operation={`subscription-grant:${account.id}`}
      title={t("subscriptions.grant.title", { email: account.owner.email })}
      intro={<p>{t("subscriptions.grant.intro")}</p>}
      action={t("subscriptions.grant.action")}
      fields={(error) => <GrantFields plans={plans} canReadPlans={canReadPlans} currency={currency} error={error} />}
      read={(values) => readGrant(values, currency, words)}
      send={(grant, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/accounts/{account_id}/subscription-grants", {
            params: { path: { account_id: account.id }, header: { "Idempotency-Key": key } },
            body: { ...grant, reason },
          }),
        )
      }
      explain={(error) =>
        error instanceof ApiError && error.code === "conflict" ? t("subscriptions.grant.conflict") : undefined
      }
      done={(sub) => (
        <>
          <Text>{t("subscriptions.grant.done", { email: account.owner.email, plan: sub.plan_name })}</Text>
          <Text size="sm" tone="faint">
            {t("subscriptions.grant.subscriptionId")}
            {": "}
            <Mono>{sub.id}</Mono>
            {" · "}
            {t("subscriptions.grant.validUntil")}
            {": "}
            <Time at={sub.ends_at} />
          </Text>
        </>
      )}
      onClose={onClose}
    />
  );
}

function GrantFields({
  plans,
  canReadPlans,
  currency,
  error,
}: {
  plans: AdminSchemas["SubscriptionPlan"][];
  canReadPlans: boolean;
  currency: string;
  error: unknown;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const format = useFormat();
  const duration = useDuration();
  const [source, setSource] = useState("gift");
  const offline = source === "offline_payment";
  return (
    <>
      {canReadPlans && plans.length > 0 ? (
        <Select
          label={t("subscriptions.grant.plan")}
          hint={t("subscriptions.grant.planHint")}
          name="plan_id"
          defaultValue={plans[0]?.id ?? ""}
          error={errors.field(error, "plan_id")}
        >
          {plans.map((plan) => (
            <option key={plan.id} value={plan.id}>
              {`${plan.name} · ${format.money(plan.price)} · ${duration(plan.term_seconds)}`}
            </option>
          ))}
        </Select>
      ) : (
        <Field
          label={t("subscriptions.grant.planId")}
          hint={t("subscriptions.grant.planIdHint")}
          name="plan_id"
          autoComplete="off"
          error={errors.field(error, "plan_id")}
        />
      )}
      <Select
        label={t("subscriptions.grant.source")}
        hint={t(offline ? "subscriptions.grant.offlineHint" : "subscriptions.grant.giftHint")}
        name="source"
        value={source}
        onChange={(event) => setSource(event.target.value)}
      >
        <option value="gift">{t("subscriptions.grant.gift")}</option>
        <option value="offline_payment">{t("subscriptions.grant.offline")}</option>
      </Select>
      {offline ? <OfflinePaymentFields currency={currency} error={error} /> : null}
    </>
  );
}

function OfflinePaymentFields({ currency, error }: { currency: string; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [paidIn, setPaidIn] = useState(currency);
  return (
    <>
      <Select
        label={t("subscriptions.grant.currency")}
        name="currency"
        value={paidIn}
        onChange={(event) => setPaidIn(event.target.value)}
        error={errors.field(error, "payment/amount/currency")}
        options={paymentCurrencies.map((code) => ({ value: code, label: code }))}
      />
      <Field
        label={t("subscriptions.grant.paid", { currency: paidIn })}
        hint={t("subscriptions.grant.paidHint")}
        name="amount"
        inputMode="decimal"
        autoComplete="off"
        error={errors.field(error, "payment") ?? errors.field(error, "payment/amount")}
      />
      <Field
        label={t("subscriptions.grant.channel")}
        hint={t("subscriptions.grant.channelHint")}
        name="channel"
        maxLength={200}
        autoComplete="off"
        error={errors.field(error, "payment/channel")}
      />
    </>
  );
}

function readRefundRequest(
  values: Record<string, string>,
  words: { grantIdInvalid: string; creditsInvalid: string },
): { grant_id: string; credits: string } | Problem {
  const grant_id = (values.grant_id ?? "").trim().toLowerCase();
  if (!uuidPattern.test(grant_id)) {
    return { problem: words.grantIdInvalid };
  }
  const credits = parseMoney(values.credits ?? "");
  if (credits === null || /^0+$/.test(credits)) {
    return { problem: words.creditsInvalid };
  }
  return { grant_id, credits };
}

export function RequestSubscriptionRefundDialog({ account, onClose }: { account: Account; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const currency = account.balance.balance.currency;
  const words = {
    grantIdInvalid: t("subscriptions.refund.grantIdInvalid"),
    creditsInvalid: t("subscriptions.refund.creditsInvalid"),
  };
  return (
    <ChangeDialog
      operation={`subscription-refund-request:${account.id}`}
      title={t("subscriptions.refund.requestTitle")}
      intro={<p>{t("subscriptions.refund.requestIntro")}</p>}
      action={t("subscriptions.refund.requestAction")}
      fields={(error) => (
        <>
          <Field
            label={t("subscriptions.refund.grantId")}
            hint={t("subscriptions.refund.grantIdHint")}
            name="grant_id"
            autoComplete="off"
            error={errors.field(error, "grant_id")}
          />
          <Field
            label={t("subscriptions.refund.credits", { currency })}
            hint={t("subscriptions.refund.creditsHint")}
            name="credits"
            inputMode="decimal"
            autoComplete="off"
            error={errors.field(error, "credits")}
          />
        </>
      )}
      read={(values) => readRefundRequest(values, words)}
      send={({ grant_id, credits }, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/accounts/{account_id}/subscription-grants/{grant_id}/refunds", {
            params: { path: { account_id: account.id, grant_id }, header: { "Idempotency-Key": key } },
            body: { credits, reason },
          }),
        )
      }
      explain={(error) =>
        error instanceof ApiError && error.code === "conflict" ? t("subscriptions.refund.conflict") : undefined
      }
      done={(proposal) => <RefundProposed proposal={proposal} />}
      onClose={onClose}
    />
  );
}

function RefundProposed({ proposal }: { proposal: AdminSchemas["SubscriptionRefundProposal"] }) {
  const { t } = useTranslation();
  return (
    <>
      <Text>
        {t("subscriptions.refund.proposed", { state: t(`subscriptions.refund.proposalState.${proposal.state}`) })}
      </Text>
      <Cluster>
        <Text size="sm" tone="faint">
          {t("subscriptions.refund.proposalId")}
          {": "}
          <Mono>{proposal.id}</Mono>
        </Text>
        <CopyButton text={proposal.id} label={t("subscriptions.refund.copyProposalId")} />
      </Cluster>
    </>
  );
}

export function ApproveSubscriptionRefundDialog({ account, onClose }: { account: Account; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const format = useFormat();
  const currency = account.balance.balance.currency;
  return (
    <ChangeDialog
      operation={`subscription-refund-approve:${account.id}`}
      title={t("subscriptions.refund.approveTitle")}
      intro={<p>{t("subscriptions.refund.approveIntro")}</p>}
      action={t("subscriptions.refund.approveAction")}
      fields={(error) => (
        <Field
          label={t("subscriptions.refund.refundId")}
          hint={t("subscriptions.refund.refundIdHint")}
          name="refund_id"
          autoComplete="off"
          error={errors.field(error, "refund_id")}
        />
      )}
      read={(values): { refund_id: string } | Problem => {
        const refund_id = (values.refund_id ?? "").trim().toLowerCase();
        return uuidPattern.test(refund_id) ? { refund_id } : { problem: t("subscriptions.refund.refundIdInvalid") };
      }}
      send={({ refund_id }, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/accounts/{account_id}/subscription-refunds/{refund_id}/approve", {
            params: { path: { account_id: account.id, refund_id }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      explain={(error) =>
        error instanceof ApiError && error.code === "conflict" ? t("subscriptions.refund.conflict") : undefined
      }
      done={(refund) => (
        <>
          <Text>
            {t("subscriptions.refund.approved", {
              credits: format.money({ amount: refund.credits, currency }),
              amount: format.money(refund.amount),
            })}
          </Text>
          <Text size="sm" tone="faint">
            {t("subscriptions.refund.approvedDetails", {
              windows: format.number(refund.windows),
              unused: format.money({ amount: refund.unused, currency }),
              revoked: t(refund.revoked ? "subscriptions.refund.yes" : "subscriptions.refund.no"),
            })}
          </Text>
        </>
      )}
      onClose={onClose}
    />
  );
}
