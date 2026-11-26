import type { AdminSchemas } from "@axiom/api";
import { LoadMore } from "@axiom/app";
import { Card, DataState, DataTable, EmptyState, Field, Id, Mono, PageHead, Text, Tooltip, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { AuditDrawer } from "../audit-drawer";
import { useEventLabel } from "../audit-events";
import en from "../locales/en/audit";
import enEvents from "../locales/en/audit-events";
import zh from "../locales/zh/audit";
import zhEvents from "../locales/zh/audit-events";
import { usePanelSteps } from "../panel-steps";
import { FilterBar, PageBody, text, toInstant, toLocalInput } from "../parts";
import { adminAuditQuery } from "../queries";

// The page's words, and the words for the actions, are loaded with it: they would otherwise weigh on the first screen.
export const messages = {
  zh: { audit: zh, auditEvents: zhEvents },
  en: { audit: en, auditEvents: enEvents },
};

type Entry = AdminSchemas["AuditEntry"];

// The audit log is read-only and immutable: a search over exact fields, newest first, with an entry's detail in a
// drawer over the list. It is a separate permission from calls, so support can answer a bill without reading every
// operator's changes.
export default function Audit() {
  const { t } = useTranslation();
  const filters = useSearch({ from: "/app/audit" });
  const audit = useInfiniteQuery(adminAuditQuery(filters));
  const rows = audit.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  const narrowed = Object.values(filters).some(Boolean);
  const [selected, select] = useState<string>();
  const open = rows.find((entry) => entry.id === selected);
  const steps = usePanelSteps(rows, selected ?? "", (entry) => entry.id, select);
  return (
    <>
      <PageBody>
        <PageHead title={t("shell.nav.audit")} lede={t("audit.description")} />
        <Filters filters={filters} />
        <Card layout="flush">
          <DataState
            query={audit}
            isEmpty={() => rows.length === 0}
            empty={
              <EmptyState title={narrowed ? t("audit.noMatch.title") : t("audit.empty.title")}>
                {narrowed ? t("audit.noMatch.body") : t("audit.empty.body")}
              </EmptyState>
            }
          >
            {() => <AuditTable rows={rows} selected={selected} onOpen={(entry) => select(entry.id)} />}
          </DataState>
          <LoadMore list={audit} />
        </Card>
      </PageBody>
      {open ? <AuditDrawer entry={open} steps={steps} onClose={() => select(undefined)} /> : null}
    </>
  );
}

type FiltersValue = {
  since?: string;
  until?: string;
  action?: string;
  module?: string;
  actor_kind?: string;
  actor_id?: string;
  target_kind?: string;
  target_id?: string;
  account_id?: string;
  site_id?: string;
  request_id?: string;
};

function Filters({ filters }: { filters: FiltersValue }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  return (
    <FilterBar
      values={filters}
      onApply={(values) =>
        navigate({
          to: "/audit",
          search: {
            since: toInstant(values.since),
            until: toInstant(values.until),
            action: text(values.action),
            module: text(values.module),
            actor_kind: text(values.actor_kind),
            actor_id: text(values.actor_id),
            target_kind: text(values.target_kind),
            target_id: text(values.target_id),
            account_id: text(values.account_id),
            site_id: text(values.site_id),
            request_id: text(values.request_id),
          },
        })
      }
    >
      <Field
        label={t("audit.filters.since")}
        name="since"
        type="datetime-local"
        defaultValue={toLocalInput(filters.since)}
      />
      <Field
        label={t("audit.filters.until")}
        name="until"
        type="datetime-local"
        defaultValue={toLocalInput(filters.until)}
      />
      <Field label={t("audit.filters.action")} name="action" defaultValue={filters.action} maxLength={200} />
      <Field label={t("audit.filters.module")} name="module" defaultValue={filters.module} maxLength={200} />
      <Field label={t("audit.filters.actorKind")} name="actor_kind" defaultValue={filters.actor_kind} maxLength={200} />
      <Field label={t("audit.filters.actor")} name="actor_id" defaultValue={filters.actor_id} maxLength={200} />
      <Field
        label={t("audit.filters.targetKind")}
        name="target_kind"
        defaultValue={filters.target_kind}
        maxLength={200}
      />
      <Field label={t("audit.filters.target")} name="target_id" defaultValue={filters.target_id} maxLength={200} />
      <Field label={t("audit.filters.account")} name="account_id" defaultValue={filters.account_id} />
      <Field label={t("audit.filters.site")} name="site_id" defaultValue={filters.site_id} />
      <Field label={t("audit.filters.request")} name="request_id" defaultValue={filters.request_id} />
    </FilterBar>
  );
}

// The action in words; one the words do not know is a generic label with its own name under it, in mono.
function Action({ entry }: { entry: Entry }) {
  const label = useEventLabel()(entry.action);
  return (
    <>
      <b>{label.text}</b>
      {label.raw ? (
        <Text as="div" size="sm" tone="faint">
          <Mono>{label.raw}</Mono>
        </Text>
      ) : null}
    </>
  );
}

function AuditTable({
  rows,
  selected,
  onOpen,
}: {
  rows: Entry[];
  selected: string | undefined;
  onOpen: (entry: Entry) => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <DataTable<Entry>
      label={t("shell.nav.audit")}
      rows={rows}
      rowKey={(entry) => entry.id ?? ""}
      onOpen={onOpen}
      selected={selected}
      columns={[
        {
          key: "at",
          header: t("audit.col.at"),
          cell: (entry) => (entry.at ? format.dateTime(new Date(entry.at)) : "—"),
        },
        { key: "action", header: t("audit.col.action"), cell: (entry) => <Action entry={entry} /> },
        {
          key: "actor",
          header: t("audit.col.actor"),
          from: "lg",
          cell: (entry) => (entry.actor_id ? <Id value={entry.actor_id} /> : "—"),
        },
        {
          key: "target",
          header: t("audit.col.target"),
          from: "md",
          cell: (entry) =>
            entry.target_id ? (
              <Tooltip text={[entry.target_kind, entry.target_id].filter(Boolean).join(" ")} width={180} />
            ) : (
              "—"
            ),
        },
        {
          key: "reason",
          header: t("audit.col.reason"),
          from: "xl",
          muted: true,
          cell: (entry) => (entry.reason ? <Tooltip text={entry.reason} width={240} /> : "—"),
        },
      ]}
    />
  );
}
