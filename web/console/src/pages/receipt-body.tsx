import type { Schemas } from "@axiom/api";
import { Card, Cluster, EmptyState, SettingRow, Stack, useFormat } from "@axiom/ui";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { receiptState } from "../receipt";

export function ReceiptBody({ order, site, actions }: { order: Schemas["Order"]; site: string; actions?: ReactNode }) {
  const { t } = useTranslation();
  const format = useFormat();
  const state = receiptState(order);

  if (state.kind !== "ready") {
    return (
      <Card title={t("receipt.title")}>
        <EmptyState title={t(`receipt.none.${state.kind}.title`)}>{t(`receipt.none.${state.kind}.body`)}</EmptyState>
      </Card>
    );
  }

  const refunds = state.refunds.filter((refund) => refund.state === "succeeded");

  return (
    <Card title={t("receipt.title")} actions={actions} layout="rows">
      <SettingRow title={t("receipt.order")}>
        <span>{order.id}</span>
      </SettingRow>
      <SettingRow title={t("receipt.paidAt")}>
        <span>{format.dateTime(state.paidAt)}</span>
      </SettingRow>
      <SettingRow title={t("receipt.method")}>
        <span>{order.method}</span>
      </SettingRow>
      <SettingRow title={t("receipt.paid")}>
        <strong>{format.pay(state.paid)}</strong>
      </SettingRow>
      <SettingRow title={t("receipt.credited")}>
        <strong>{format.money(state.credited)}</strong>
      </SettingRow>
      <SettingRow title={t("receipt.refunds")}>
        {refunds.length === 0 ? (
          <span>{t("receipt.noRefunds")}</span>
        ) : (
          <Stack gap={4}>
            {refunds.map((refund) => (
              <Cluster key={refund.id} gap={8}>
                <span>
                  {t("receipt.refundLine", {
                    amount: format.pay(refund.amount),
                    credits: format.money(refund.credits),
                  })}
                </span>
                <span>{format.dateTime(new Date(refund.finished_at ?? refund.created_at))}</span>
              </Cluster>
            ))}
          </Stack>
        )}
      </SettingRow>
      <SettingRow title={site}>
        <span>{t("receipt.footnote", { site })}</span>
      </SettingRow>
    </Card>
  );
}
