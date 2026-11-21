import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { Dialog } from "@axiom/boards/ds";
import {
  ActionButton,
  Card,
  DataState,
  EmptyState,
  LoadMore,
  PageHead,
  Stack,
  Table,
  Td,
  Text,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import en from "../locales/en/alert-events";
import zh from "../locales/zh/alert-events";
import { Code, Facts, Head, Json, PageBody, State, Time } from "../parts";
import { alertEventsQuery } from "../policy-queries";

export const messages = { zh: { alertEvents: zh }, en: { alertEvents: en } };

type Event = AdminSchemas["AlertEvent"];
type Receipt = AdminSchemas["AlertReceipt"];

export default function AlertEvents() {
  const { t } = useTranslation();
  const query = useInfiniteQuery(alertEventsQuery);
  const [selected, setSelected] = useState<string>();
  const [resolving, setResolving] = useState<string>();
  const rows = query.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  const event = query.isSuccess && selected ? rows.find((row) => row.id === selected) : undefined;
  return (
    <>
      <PageHead title={t("nav.alertEvents")} lede={t("alertEvents.description")} />
      <PageBody>
        <Card layout="flush">
          <DataState
            query={query}
            isEmpty={() => rows.length === 0}
            empty={<EmptyState title={t("alertEvents.empty")} />}
          >
            {() => <Events rows={rows} select={setSelected} />}
          </DataState>
          <LoadMore list={query} />
        </Card>
      </PageBody>
      {resolving ? (
        <Resolve
          id={resolving}
          onClose={() => {
            setResolving(undefined);
            setSelected(undefined);
          }}
        />
      ) : event ? (
        <Details event={event} resolve={setResolving} onClose={() => setSelected(undefined)} />
      ) : null}
    </>
  );
}

const eventTone = { firing: "bad", delivered: "warn", resolved: "good" } as const;
function Events({ rows, select }: { rows: Event[]; select: (id: string) => void }) {
  const { t } = useTranslation();
  return (
    <Table label={t("nav.alertEvents")}>
      <Head
        cols={[
          [t("alertEvents.kind"), { main: true }],
          t("alertEvents.severityLabel"),
          t("alertEvents.status"),
          t("alertEvents.fired"),
        ]}
        action
      />
      <tbody>
        {rows.map((row) => {
          if (!row.id) return null;
          const id = row.id;
          const knownSeverity =
            row.severity === "info" || row.severity === "warning" || row.severity === "critical"
              ? row.severity
              : undefined;
          return (
            <tr key={id}>
              <Td main>
                <Text>{row.kind}</Text>
                <Code>{row.key}</Code>
              </Td>
              <Td>{knownSeverity ? t(`alertEvents.severity.${knownSeverity}`) : row.severity}</Td>
              <Td>
                {row.state ? <State tone={eventTone[row.state]}>{t(`alertEvents.state.${row.state}`)}</State> : "—"}
              </Td>
              <Td>
                <Time at={row.fired_at} />
              </Td>
              <Td actions>
                <ActionButton size="sm" onClick={() => select(id)}>
                  {t("alertEvents.details")}
                </ActionButton>
              </Td>
            </tr>
          );
        })}
      </tbody>
    </Table>
  );
}

function Details({ event, resolve, onClose }: { event: Event; resolve: (id: string) => void; onClose: () => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  const id = event.id;
  return (
    <Dialog
      title={t("alertEvents.details")}
      closeLabel={t("dialog.close", { ns: "common" })}
      onClose={onClose}
      primary={
        id && event.state && event.state !== "resolved"
          ? { label: t("alertEvents.resolve"), onClick: () => resolve(id) }
          : { label: t("change.close"), onClick: onClose }
      }
    >
      <Stack>
        <Facts
          rows={[
            [t("alertEvents.id"), <Code key="id">{id}</Code>],
            [t("alertEvents.kind"), event.kind],
            [t("alertEvents.occurrences"), event.occurrences === undefined ? "—" : format.number(event.occurrences)],
            [t("alertEvents.fired"), <Time key="fired" at={event.fired_at} />],
            [t("alertEvents.resolved"), <Time key="resolved" at={event.resolved_at} />],
          ]}
        />
        {event.fields && Object.keys(event.fields).length ? (
          <Json label={t("alertEvents.fields")} value={event.fields} />
        ) : null}
        <Receipts rows={event.receipts ?? []} />
      </Stack>
    </Dialog>
  );
}

function Receipts({ rows }: { rows: Receipt[] }) {
  const { t } = useTranslation();
  return (
    <Card title={t("alertEvents.receipts")} layout="flush">
      {rows.length ? (
        <Table label={t("alertEvents.receipts")}>
          <Head cols={[[t("alertEvents.rule"), { main: true }], t("alertEvents.status"), t("alertEvents.receiptAt")]} />
          <tbody>
            {rows.map((row) => (
              <tr key={row.id ?? `${row.rule_id}/${row.sequence}`}>
                <Td main>
                  <Text>{row.rule_id}</Text>
                  <Code>{row.id}</Code>
                </Td>
                <Td>{row.state ? t(`alertEvents.receipt.${row.state}`) : "—"}</Td>
                <Td>
                  <Time at={row.delivered_at ?? row.at} />
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      ) : (
        <EmptyState title={t("alertEvents.noReceipts")} />
      )}
    </Card>
  );
}

function Resolve({ id, onClose }: { id: string; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`resolve-alert:${id}`}
      title={t("alertEvents.resolve")}
      action={t("alertEvents.resolve")}
      intro={<Code>{id}</Code>}
      read={() => id}
      send={(event_id, { key, reason }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/alerts/events/{event_id}/resolve", {
            params: { path: { event_id }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={() => <p>{t("alertEvents.resolvedDone")}</p>}
      onClose={onClose}
    />
  );
}
