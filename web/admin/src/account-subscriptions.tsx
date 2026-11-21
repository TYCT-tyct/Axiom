import { type AdminSchemas, ApiError } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { ActionButton, Card, Cluster, DataState, EmptyState, Id, Stack, Table, Td, Text, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import {
  ApproveSubscriptionRefundDialog,
  GrantSubscriptionDialog,
  RequestSubscriptionRefundDialog,
} from "./account-subscription-dialogs";
import { Head, State, SubLine } from "./parts";
import { accountSubscriptionsQuery } from "./subscription-queries";

type Account = AdminSchemas["AccountDetail"];
type Subscription = AdminSchemas["Subscription"];
type DialogKind = "grant" | "refund-request" | "refund-approve" | null;

// AccountSubscriptions shows an account's active and expired subscriptions with their current window usage, plus the
// grant and two-person refund workflows when the session holds credits.grant.
export function AccountSubscriptions({ account }: { account: Account }) {
  const { t } = useTranslation();
  const canGrant = can(useSession(), "credits.grant");
  const query = useQuery(accountSubscriptionsQuery(account.id));
  const [open, setOpen] = useState<DialogKind>(null);
  const currency = account.balance.balance.currency;

  if (query.error instanceof ApiError && query.error.status === 404) {
    return (
      <Card title={t("subscriptions.account.title")}>
        <Text tone="muted">{t("subscriptions.account.unavailable")}</Text>
      </Card>
    );
  }

  const actions = canGrant ? (
    <Cluster>
      <ActionButton variant="pri" size="sm" onClick={() => setOpen("grant")}>
        {t("subscriptions.account.grant")}
      </ActionButton>
      <ActionButton size="sm" onClick={() => setOpen("refund-request")}>
        {t("subscriptions.account.refundRequest")}
      </ActionButton>
      <ActionButton size="sm" onClick={() => setOpen("refund-approve")}>
        {t("subscriptions.account.refundApprove")}
      </ActionButton>
    </Cluster>
  ) : undefined;

  return (
    <>
      <Card
        title={t("subscriptions.account.title")}
        lede={t("subscriptions.account.description")}
        layout="flush"
        actions={actions}
      >
        <DataState
          query={query}
          isEmpty={(data) => data.data.length === 0}
          empty={
            <EmptyState title={t("subscriptions.account.emptyTitle")}>
              {t("subscriptions.account.emptyBody")}
            </EmptyState>
          }
        >
          {(data) => <SubscriptionsTable subscriptions={data.data} currency={currency} />}
        </DataState>
      </Card>
      {open === "grant" ? <GrantSubscriptionDialog account={account} onClose={() => setOpen(null)} /> : null}
      {open === "refund-request" ? (
        <RequestSubscriptionRefundDialog account={account} onClose={() => setOpen(null)} />
      ) : null}
      {open === "refund-approve" ? (
        <ApproveSubscriptionRefundDialog account={account} onClose={() => setOpen(null)} />
      ) : null}
    </>
  );
}

function SubscriptionsTable({ subscriptions, currency }: { subscriptions: Subscription[]; currency: string }) {
  const { t } = useTranslation();
  return (
    <Table label={t("subscriptions.account.title")}>
      <Head
        cols={[
          [t("subscriptions.account.col.plan"), { main: true }],
          t("subscriptions.account.col.state"),
          [t("subscriptions.account.col.window"), { main: true }],
          t("subscriptions.account.col.term"),
        ]}
      />
      <tbody>
        {subscriptions.map((sub) => (
          <SubscriptionRow key={sub.id} sub={sub} currency={currency} />
        ))}
      </tbody>
    </Table>
  );
}

function SubscriptionRow({ sub, currency }: { sub: Subscription; currency: string }) {
  const { t } = useTranslation();
  const format = useFormat();
  const money = (amount: string) => format.money({ amount, currency });
  const w = sub.current_window;
  return (
    <tr>
      <Td main>
        <Stack gap={4}>
          <Text as="span">{sub.plan_name}</Text>
          <Id value={sub.id} />
        </Stack>
      </Td>
      <Td>
        <State tone={sub.state === "active" ? "good" : "neutral"}>
          {t(`subscriptions.account.state.${sub.state}`)}
        </State>
      </Td>
      <Td main>
        <Text as="div">
          {t("subscriptions.account.windowSummary", { seq: w.seq, used: money(w.used), quota: money(w.quota) })}
        </Text>
        <SubLine>
          {t("subscriptions.account.windowDetail", {
            remaining: money(w.remaining),
            reserved: money(w.reserved),
            ends: format.dateTimeMinute(new Date(w.ends_at)),
          })}
        </SubLine>
      </Td>
      <Td shrink>
        <Text as="div">{format.dateTimeMinute(new Date(sub.starts_at))}</Text>
        <SubLine>
          {t("subscriptions.account.termUntil", { ends: format.dateTimeMinute(new Date(sub.ends_at)) })}
        </SubLine>
        {sub.closed_at ? (
          <SubLine>
            {t("subscriptions.account.closedAt", { at: format.dateTimeMinute(new Date(sub.closed_at)) })}
          </SubLine>
        ) : null}
      </Td>
    </tr>
  );
}
