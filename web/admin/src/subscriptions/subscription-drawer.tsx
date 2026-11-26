import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { ActionButton, DrawerFacts, DrawerSection, QuotaMeter, Text, TextLink, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ApproveSubscriptionRefundDialog, RequestSubscriptionRefundDialog } from "../account-subscription-dialogs";
import { Code, State } from "../parts";
import { accountQuery } from "../queries";
import { RowDrawer, type useSelected } from "../row-drawer";
import { daysLeft, Ends, usedShare } from "./ends";

type Subscription = AdminSchemas["Subscription"];
type Dialog = "request" | "approve";

// One subscription in a drawer: how much of the window's quota is used and what happens past it, who holds it and until
// when. A refund takes two people, and the drawer keeps the existing flow: one applies for it, and another, a different
// signed-in operator, approves it by its number. The dialogs are the account's, so the account is read to open them.
export function SubscriptionDrawer({
  sub,
  keys,
  select,
  now,
}: {
  sub: Subscription;
  keys: readonly string[];
  select: ReturnType<typeof useSelected>[1];
  now: number;
}) {
  const { t } = useTranslation();
  return (
    <RowDrawer
      title={t("subscriptions.drawer.title", { plan: sub.plan_name })}
      subtitle={t("subscriptions.drawer.subtitle", { id: sub.account_id.slice(-8) })}
      keys={keys}
      selected={sub.id}
      select={select}
      footer={<Refunds sub={sub} />}
    >
      <Body sub={sub} now={now} />
    </RowDrawer>
  );
}

function Body({ sub, now }: { sub: Subscription; now: number }) {
  const { t } = useTranslation();
  const format = useFormat();
  const account = useQuery(accountQuery(sub.account_id));
  const currency = account.data?.balance.balance.currency;
  const window = sub.current_window;
  // The credit is the site's currency; the account says which, and until it answers the figures are plain numbers.
  const money = (amount: string) =>
    currency ? format.money({ amount, currency }) : format.number(Number(amount) / 1e9);
  const over = BigInt(window.remaining) === 0n && BigInt(window.quota) > 0n;
  return (
    <>
      <QuotaMeter
        label={t("subscriptions.drawer.quota")}
        used={money(window.used)}
        of={money(window.quota)}
        share={usedShare(sub)}
        over={over}
        note={over ? t("subscriptions.drawer.over") : t("subscriptions.drawer.resets")}
      />
      <DrawerFacts
        rows={[
          [
            t("subscriptions.col.state"),
            <State key="state" tone={sub.state === "active" ? "good" : "neutral"}>
              {t(`subscriptions.account.state.${sub.state}`)}
            </State>,
          ],
          [
            t("subscriptions.col.account"),
            <TextLink key="account">
              <Link to="/users/accounts" search={{ selected: sub.account_id }}>
                <Code title={sub.account_id}>{sub.account_id}</Code>
              </Link>
            </TextLink>,
          ],
          [t("subscriptions.col.started"), format.dateTime(new Date(sub.starts_at))],
          [t("subscriptions.col.ends"), <Ends key="ends" sub={sub} now={now} />],
          [t("subscriptions.drawer.window"), t("subscriptions.drawer.windowSeq", { seq: window.seq })],
          [t("subscriptions.drawer.remaining"), money(window.remaining)],
          [t("subscriptions.drawer.reserved"), money(window.reserved)],
          ...(sub.closed_at
            ? [[t("subscriptions.drawer.closed"), format.dateTime(new Date(sub.closed_at))] as const]
            : []),
          [t("subscriptions.drawer.id"), <Code key="id">{sub.id}</Code>],
        ]}
      />
      <DrawerSection title={t("subscriptions.drawer.refundTitle")}>
        <Text size="sm" tone="muted">
          {daysLeft(sub.ends_at, now) < 0 ? t("subscriptions.drawer.refundEnded") : t("subscriptions.drawer.refundTwo")}
        </Text>
      </DrawerSection>
    </>
  );
}

// The two halves of a refund, each its own dialog and each by what the contract has: the grant to refund and how much
// of it, from one operator; the refund's number, from another. Only a session with credits.grant is offered them.
function Refunds({ sub }: { sub: Subscription }) {
  const { t } = useTranslation();
  const allowed = can(useSession(), "credits.grant");
  const account = useQuery({ ...accountQuery(sub.account_id), enabled: allowed });
  const [dialog, setDialog] = useState<Dialog>();
  if (!allowed || !account.data) return null;
  const close = () => setDialog(undefined);
  return (
    <>
      <ActionButton variant="gho" onClick={() => setDialog("request")}>
        {t("subscriptions.account.refundRequest")}
      </ActionButton>
      <ActionButton onClick={() => setDialog("approve")}>{t("subscriptions.account.refundApprove")}</ActionButton>
      {dialog === "request" ? <RequestSubscriptionRefundDialog account={account.data} onClose={close} /> : null}
      {dialog === "approve" ? <ApproveSubscriptionRefundDialog account={account.data} onClose={close} /> : null}
    </>
  );
}
