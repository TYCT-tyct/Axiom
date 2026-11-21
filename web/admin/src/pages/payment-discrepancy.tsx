import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { ActionButton, DataState, EmptyState, Icon, PageHead, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import { discrepancyQuery } from "../finance-queries";
import en from "../locales/en/payment-discrepancies";
import zh from "../locales/zh/payment-discrepancies";
import { formatJSON } from "../lossless";
import { Actions, Code, Facts, PageBody, State, Time } from "../parts";

export const messages = { zh: { discrepancies: zh }, en: { discrepancies: en } };

type Row = AdminSchemas["PaymentDiscrepancy"];

const evidenceFields = ["trade_no", "expected", "actual", "unit"] as const;

export default function PaymentDiscrepancy() {
  const { t } = useTranslation();
  const { discrepancyId } = useParams({ from: "/app/payment-discrepancies/$discrepancyId" });
  const query = useQuery(discrepancyQuery(discrepancyId));
  return (
    <>
      <PageHead title={t("discrepancies.detailTitle")} lede={t("discrepancies.investigationOnly")} />
      <PageBody>
        <Actions>
          <ActionButton asChild>
            <Link to="/payment-discrepancies">
              <Icon name="left" />
              {t("discrepancies.back")}
            </Link>
          </ActionButton>
        </Actions>
        <DataState
          query={query}
          isEmpty={(row) => !row.id}
          empty={<EmptyState title={t("discrepancies.unavailable")} />}
        >
          {(row) => <Details row={row} id={discrepancyId} />}
        </DataState>
      </PageBody>
    </>
  );
}

function Details({ row, id }: { row: Row; id: string }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const evidence = Object.entries(row.detail ?? {}).map(([key, value]) => {
    const name = evidenceFields.find((field) => field === key);
    return [
      name ? t(`discrepancies.facts.${name}`) : key,
      <Text key={key} breaks>
        {typeof value === "string" ? value : formatJSON(value)}
      </Text>,
    ] as const;
  });
  return (
    <>
      <Facts
        title={t("discrepancies.summary")}
        actions={
          row.state === "open" ? (
            <ActionButton variant="pri" onClick={() => setOpen(true)}>
              {t("discrepancies.investigate")}
            </ActionButton>
          ) : undefined
        }
        rows={[
          [t("discrepancies.id"), <Code key="id">{id}</Code>],
          [t("discrepancies.kind"), row.kind ?? "—"],
          [
            t("discrepancies.status"),
            row.state ? (
              <State key="state" tone={row.state === "open" ? "warn" : "good"}>
                {t(`discrepancies.state.${row.state}`)}
              </State>
            ) : (
              "—"
            ),
          ],
          [t("discrepancies.order"), <Code key="order">{row.order_id ?? "—"}</Code>],
          [t("discrepancies.merchant"), <Code key="merchant">{row.merchant_id ?? "—"}</Code>],
          [t("discrepancies.account"), <Code key="account">{row.account_id ?? "—"}</Code>],
          [t("discrepancies.created"), <Time key="created" at={row.created_at} />],
        ]}
      />
      {evidence.length > 0 ? <Facts title={t("discrepancies.evidence")} rows={evidence} /> : null}
      {row.state === "resolved" ? <Investigation row={row} /> : null}
      {open ? <Investigate id={id} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

function Investigation({ row }: { row: Row }) {
  const { t } = useTranslation();
  return (
    <Facts
      title={t("discrepancies.investigation")}
      rows={[
        [t("discrepancies.by"), row.investigated_by ?? "—"],
        [t("discrepancies.investigatedAt"), <Time key="at" at={row.investigated_at} />],
        [
          t("discrepancies.note"),
          <Text key="note" breaks>
            {row.note ?? "—"}
          </Text>,
        ],
      ]}
    />
  );
}

function Investigate({ id, onClose }: { id: string; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`investigate-payment-discrepancy:${id}`}
      title={t("discrepancies.investigate")}
      action={t("discrepancies.record")}
      intro={<p>{t("discrepancies.reasonIntro")}</p>}
      read={() => id}
      send={(discrepancy_id, { key, reason }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/payments/discrepancies/{discrepancy_id}/investigate", {
            params: { path: { discrepancy_id }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={() => <p>{t("discrepancies.recorded")}</p>}
      onClose={onClose}
    />
  );
}
