import { can, useSession } from "@axiom/app";
import type BillDesktop from "@axiom/boards/AxApp/regions/Bill";
import { Card, DataState, EmptyState, Grid, Metric, Note, Stack, useFormat, usePageTitle } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useLocation, useNavigate } from "@tanstack/react-router";
import { type MouseEvent, type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { balanceQuery } from "../queries";
import {
  type BalanceData,
  type CreditBatch,
  formatQuickAmount,
  quickAmountsForCurrency,
  splitMoneyParts,
  totalFrozenFromBatches,
} from "./bill-data";
import { BillPlansPanel } from "./bill-plans";
import { BillRegion } from "./bill-region";
import { BillShell, BillTabs } from "./bill-shell";
import { BillTopUpModal } from "./bill-topup";
import { useBillTopUpSession } from "./bill-topup-state";

type BillProps = Parameters<typeof BillDesktop>[0];
const SOON_MS = 7 * 24 * 60 * 60 * 1000;

type BatchLabels = {
  neverExpires: string;
  expired: string;
  expiresSoon: string;
  source: Record<CreditBatch["source"], string>;
  frozen: (amount: string) => string;
  orderLink: string;
};

function formatBatchExpiry(expiresAt: string | undefined, format: ReturnType<typeof useFormat>, labels: BatchLabels) {
  if (!expiresAt) return labels.neverExpires;
  const date = new Date(expiresAt);
  const diff = date.getTime() - Date.now();
  if (diff <= 0) return `${labels.expired} · ${format.dateTimeMinute(date)}`;
  if (diff <= SOON_MS) return `${labels.expiresSoon} · ${format.dateTimeMinute(date)}`;
  return format.dateTimeMinute(date);
}

function buildBatchRows(
  batches: readonly CreditBatch[],
  format: ReturnType<typeof useFormat>,
  labels: BatchLabels,
): BillProps["rows"]["entries0"] {
  return batches.map((batch) => {
    const sourceLabel = labels.source[batch.source];
    const created = format.dateTimeMinute(new Date(batch.created_at));
    const remainingText =
      BigInt(batch.frozen.amount) > 0n
        ? `${format.money(batch.remaining)} (${labels.frozen(format.money(batch.frozen))})`
        : format.money(batch.remaining);
    const expiryText = formatBatchExpiry(batch.expires_at, format, labels);
    const initialText = format.money(batch.amount);

    if (batch.source === "paid") {
      return {
        key: batch.id,
        kind: "0" as const,
        f0: "ppled",
        f1: "orders",
        f2: batch.id,
        f3: "",
        f4: `${sourceLabel} · ${created}`,
        f5: "",
        f6: expiryText,
        f7: "ppgo",
        f8: "/console/orders",
        f9: labels.orderLink,
        f10: "r num",
        f11: remainingText,
        f12: "r num",
        f13: initialText,
      };
    }

    return {
      key: batch.id,
      kind: "1" as const,
      f0: "",
      f1: "",
      f2: `${sourceLabel} · ${created}`,
      f3: "",
      f4: expiryText,
      f5: "r num",
      f6: "",
      f7: remainingText,
      f8: "r num",
      f9: initialText,
    };
  });
}

function BalanceBreakdown({ data }: { data: BalanceData }) {
  const { t } = useTranslation();
  const currency = data.available.currency;
  const frozenAmount = totalFrozenFromBatches(data.batches);
  return (
    <Card title={t("balance.breakdown")} lede={t("balance.availableNote")}>
      <Grid columns={3} gap={16}>
        <Metric label={t("balance.balance")} value={data.balance} />
        <Metric label={t("balance.reserved")} value={data.reserved} />
        {frozenAmount !== null ? (
          <Metric label={t("statement.bill.frozenLabel")} value={{ amount: frozenAmount, currency }} />
        ) : null}
        <Metric label={t("balance.floor")} value={data.floor} />
        <Metric label={t("balance.spentThisMonth")} value={data.spent_this_month} />
      </Grid>
    </Card>
  );
}

function BatchesFallback({ batches }: { batches: BalanceData["batches"] }) {
  const { t } = useTranslation();
  if (batches === undefined) {
    return (
      <Card title={t("balance.batches")} lede={t("balance.batchesNote")}>
        <Note title={t("balance.batchesUnavailable")} />
      </Card>
    );
  }
  if (batches.length === 0) {
    return (
      <Card title={t("balance.batches")} lede={t("balance.batchesNote")}>
        <EmptyState title={t("balance.noBatches")}>{t("balance.batchesNote")}</EmptyState>
      </Card>
    );
  }
  return null;
}

function BalanceBoardContent({
  data,
  canTopUp,
  onTopUp,
  onTableClick,
  extra,
}: {
  data: BalanceData;
  canTopUp: boolean;
  onTopUp: (initialUnits?: string) => void;
  onTableClick: (event: MouseEvent<Element>) => void;
  extra: ReactNode;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const currency = data.available.currency;
  const heroParts = splitMoneyParts(format.money(data.available));
  const batches = data.batches;
  const quick0 = quickAmountsForCurrency(currency).map((units) => ({
    key: units,
    kind: "0" as const,
    f0: "button" as const,
    f1: units,
    // the amount alone, as the board writes it: the buttons sit under the top-up button they shortcut
    f2: formatQuickAmount(units, currency, format.money),
  }));
  const cols0 = [
    { key: "source", kind: "0" as const, f0: "", f1: t("balance.col.source") },
    { key: "expires", kind: "0" as const, f0: "", f1: t("balance.col.expires") },
    { key: "remaining", kind: "0" as const, f0: "r num", f1: t("balance.col.remaining") },
    { key: "amount", kind: "0" as const, f0: "r num", f1: t("balance.col.amount") },
  ];
  const entries0 = batches
    ? buildBatchRows(batches, format, {
        neverExpires: t("balance.neverExpires"),
        expired: t("balance.expired"),
        expiresSoon: t("balance.expiresSoon"),
        source: { gift: t("balance.source.gift"), paid: t("balance.source.paid"), redeem: t("balance.source.redeem") },
        frozen: (amount) => t("balance.frozen", { amount }),
        orderLink: t("statement.bill.batchOrderLink"),
      })
    : [];
  return (
    <BillRegion
      tabs={<BillTabs active="balance" />}
      extra={extra}
      text={{
        title: t("statement.bill.title"),
        subtitle: t("statement.bill.subtitle"),
        heroLabel: t("balance.available"),
        heroCurrency: heroParts.currency,
        heroWhole: heroParts.whole,
        heroCents: heroParts.cents,
        topUpLabel: t("topup.title"),
        tableTitle: t("balance.batches"),
      }}
      rows={{ quick0, cols0, entries0 }}
      values={{
        unbuilt: true,
        hideTopUp: !canTopUp,
        hideTable: !(batches && batches.length > 0),
        tableLabel: t("balance.batches"),
        tableClick: onTableClick,
        topUp: () => onTopUp(),
        quickClick: (event) => {
          const units = (event.target as Element).closest("button[data-amt]")?.getAttribute("data-amt");
          if (units) onTopUp(units);
        },
      }}
    />
  );
}

function BalanceView() {
  const { t } = useTranslation();
  const session = useSession();
  const canTopUp = can(session, "balance.top_up");
  const navigate = useNavigate();
  const query = useQuery(balanceQuery);
  const topUpSession = useBillTopUpSession(canTopUp);
  const [modal, setModal] = useState<{ open: boolean; initialUnits?: string }>({ open: false });

  const handleTableClick = (event: MouseEvent<Element>) => {
    const target = event.target as Element;
    const row = target.closest("tr[data-a='orders']");
    if (!row) return;
    event.preventDefault();
    void navigate({ to: "/orders" });
  };

  return (
    <DataState query={query} page>
      {(data) => (
        <>
          <BalanceBoardContent
            data={data}
            canTopUp={canTopUp}
            onTopUp={(initialUnits) => setModal({ open: true, initialUnits })}
            onTableClick={handleTableClick}
            extra={
              <Stack gap={16}>
                {!canTopUp ? <Note title={t("topup.forbidden")} /> : null}
                <BalanceBreakdown data={data} />
                <BatchesFallback batches={data.batches} />
              </Stack>
            }
          />
          {canTopUp ? (
            <BillTopUpModal
              open={modal.open}
              currency={data.balance.currency}
              currentBalance={data.balance}
              initialUnits={modal.initialUnits}
              session={topUpSession}
              onClose={() => setModal({ open: false })}
            />
          ) : null}
        </>
      )}
    </DataState>
  );
}

export default function BalancePage() {
  const { t } = useTranslation();
  const { hash } = useLocation();
  const isPlans = hash === "plans" || hash === "#plans";
  usePageTitle(t(isPlans ? "statement.bill.tabs.plans" : "nav.balance"));
  if (isPlans) {
    return (
      <BillShell activeTab="plans" title={t("statement.bill.tabs.plans")} subtitle={t("statement.bill.plans.subtitle")}>
        <BillPlansPanel />
      </BillShell>
    );
  }
  return <BalanceView />;
}
