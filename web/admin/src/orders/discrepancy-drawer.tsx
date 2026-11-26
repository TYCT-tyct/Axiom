import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { ActionButton, DataState, DrawerFacts, DrawerSection, SideBySide, Text, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import { discrepancyQuery } from "../finance-queries";
import { formatJSON } from "../lossless";
import { Code, State } from "../parts";
import { type IdDrawer, PendingDrawer, RowDrawer } from "../row-drawer";
import { shortId } from "./states";

type Row = AdminSchemas["PaymentDiscrepancy"];

const evidenceFields = ["trade_no", "expected", "actual", "unit"] as const;

// A fact the check recorded, as text: a string as it is, anything else as the JSON it was written, so an integer wider
// than 53 bits keeps every digit (the record is read losslessly).
const written = (value: unknown) => (typeof value === "string" ? value : formatJSON(value));

// One discrepancy in a drawer: the two records that did not agree side by side, what else the check found out, and what
// the investigation concluded. The check says what it expected and what it found (`expected`, `actual`); the drawer does
// not say which of the two is the channel's, because that depends on the kind of check. Recording the investigation
// changes no money or credit: it closes the record, and it is the existing dialog.
export function DiscrepancyDrawer({ id, keys, select }: IdDrawer) {
  const { t } = useTranslation();
  const query = useQuery(discrepancyQuery(id));
  const [investigating, setInvestigating] = useState(false);
  const row = query.data;
  if (!row?.id) {
    return (
      <PendingDrawer title={t("discrepancies.detailTitle")} select={select}>
        <DataState
          query={query}
          isEmpty={(data) => !data.id}
          empty={<Text tone="muted">{t("discrepancies.unavailable")}</Text>}
        >
          {() => null}
        </DataState>
      </PendingDrawer>
    );
  }
  return (
    <RowDrawer
      title={t("discrepancies.drawerTitle", { id: shortId(row.id) })}
      subtitle={row.kind}
      keys={keys}
      selected={id}
      select={select}
      footer={
        row.state === "open" ? (
          <ActionButton variant="pri" onClick={() => setInvestigating(true)}>
            {t("discrepancies.investigate")}
          </ActionButton>
        ) : undefined
      }
    >
      <Body row={row} />
      {investigating ? <Investigate id={row.id} onClose={() => setInvestigating(false)} /> : null}
    </RowDrawer>
  );
}

function Body({ row }: { row: Row }) {
  const { t } = useTranslation();
  const { expected, actual } = row.detail ?? {};
  return (
    <>
      <Text prose tone="muted">
        {t("discrepancies.investigationOnly")}
      </Text>
      {expected !== undefined && actual !== undefined ? (
        <SideBySide
          items={[
            { label: t("discrepancies.expected"), value: written(expected) },
            { label: t("discrepancies.actual"), value: written(actual) },
          ]}
        />
      ) : null}
      <Summary row={row} />
      <Evidence detail={row.detail} />
      {row.state === "resolved" ? <Investigation row={row} /> : null}
    </>
  );
}

function Summary({ row }: { row: Row }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <DrawerFacts
      rows={[
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
        [t("discrepancies.account"), <Code key="account">{row.account_id ?? "—"}</Code>],
        [t("discrepancies.merchant"), <Code key="merchant">{row.merchant_id ?? "—"}</Code>],
        [t("discrepancies.created"), row.created_at ? format.dateTime(new Date(row.created_at)) : "—"],
        [t("discrepancies.id"), <Code key="id">{row.id ?? "—"}</Code>],
      ]}
    />
  );
}

// What the check recorded, each labelled by its own name when the contract names it.
function Evidence({ detail }: { detail: Row["detail"] }) {
  const { t } = useTranslation();
  const facts = Object.entries(detail ?? {});
  if (facts.length === 0) return null;
  return (
    <DrawerSection title={t("discrepancies.evidence")}>
      <DrawerFacts
        rows={facts.map(([key, value]) => {
          const name = evidenceFields.find((field) => field === key);
          return [
            name ? t(`discrepancies.facts.${name}`) : key,
            <Text key={key} as="span" breaks>
              {written(value)}
            </Text>,
          ] as const;
        })}
      />
    </DrawerSection>
  );
}

function Investigation({ row }: { row: Row }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <DrawerSection title={t("discrepancies.investigation")}>
      <DrawerFacts
        rows={[
          [t("discrepancies.by"), row.investigated_by ?? "—"],
          [
            t("discrepancies.investigatedAt"),
            row.investigated_at ? format.dateTime(new Date(row.investigated_at)) : "—",
          ],
          [
            t("discrepancies.note"),
            <Text key="note" as="span" breaks>
              {row.note ?? "—"}
            </Text>,
          ],
        ]}
      />
    </DrawerSection>
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
      warn={t("discrepancies.final")}
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
