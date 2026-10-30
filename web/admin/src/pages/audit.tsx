import type { AdminSchemas } from "@axiom/api";
import { LoadMore } from "@axiom/app";
import { DataState, EmptyState, PageHeader, Panel, Table, Td, TextField } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Code, FilterBar, Head, mainCell, PageBody, Time, text, toInstant, toLocalInput } from "../parts";
import { adminAuditQuery } from "../queries";

// The audit log is read-only and immutable: a search over exact fields, newest first, with the entry's detail on its own
// page. It is a separate permission from calls, so support can answer a bill without reading every operator's changes.
export default function Audit() {
  const { t } = useTranslation();
  const filters = useSearch({ from: "/app/audit" });
  const audit = useInfiniteQuery(adminAuditQuery(filters));
  const rows = audit.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  const narrowed = Object.values(filters).some(Boolean);
  return (
    <>
      <PageHeader title={t("nav.audit")} description={t("audit.description")} />
      <PageBody>
        <Filters filters={filters} />
        <Panel>
          <DataState
            query={audit}
            isEmpty={() => rows.length === 0}
            empty={
              <EmptyState title={narrowed ? t("audit.noMatch.title") : t("audit.empty.title")}>
                {narrowed ? t("audit.noMatch.body") : t("audit.empty.body")}
              </EmptyState>
            }
          >
            {() => <AuditTable rows={rows} />}
          </DataState>
          <LoadMore list={audit} />
        </Panel>
      </PageBody>
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
      <TextField
        label={t("audit.filters.since")}
        name="since"
        type="datetime-local"
        defaultValue={toLocalInput(filters.since)}
      />
      <TextField
        label={t("audit.filters.until")}
        name="until"
        type="datetime-local"
        defaultValue={toLocalInput(filters.until)}
      />
      <TextField label={t("audit.filters.action")} name="action" defaultValue={filters.action} maxLength={200} />
      <TextField label={t("audit.filters.module")} name="module" defaultValue={filters.module} maxLength={200} />
      <TextField
        label={t("audit.filters.actorKind")}
        name="actor_kind"
        defaultValue={filters.actor_kind}
        maxLength={200}
      />
      <TextField label={t("audit.filters.actor")} name="actor_id" defaultValue={filters.actor_id} maxLength={200} />
      <TextField
        label={t("audit.filters.targetKind")}
        name="target_kind"
        defaultValue={filters.target_kind}
        maxLength={200}
      />
      <TextField label={t("audit.filters.target")} name="target_id" defaultValue={filters.target_id} maxLength={200} />
      <TextField label={t("audit.filters.account")} name="account_id" defaultValue={filters.account_id} />
      <TextField label={t("audit.filters.site")} name="site_id" defaultValue={filters.site_id} />
      <TextField label={t("audit.filters.request")} name="request_id" defaultValue={filters.request_id} />
    </FilterBar>
  );
}

function AuditTable({ rows }: { rows: AdminSchemas["AuditEntry"][] }) {
  const { t } = useTranslation();
  return (
    <Table label={t("nav.audit")}>
      <Head
        cols={[
          [t("audit.col.at")],
          [t("audit.col.action")],
          [t("audit.col.actor"), "hidden lg:table-cell"],
          [t("audit.col.target"), "hidden md:table-cell"],
          [t("audit.col.request"), "hidden xl:table-cell"],
        ]}
      />
      <tbody>
        {rows.map((entry, index) => (
          <tr key={entry.id ?? index}>
            <Td>
              {entry.id ? (
                <Link to="/audit/$entryId" params={{ entryId: entry.id }} className="text-accent hover:underline">
                  <Time at={entry.at} />
                </Link>
              ) : (
                <Time at={entry.at} />
              )}
            </Td>
            <Td className={mainCell}>
              <span className="block truncate" title={entry.action}>
                <Code>{entry.action ?? "—"}</Code>
              </span>
              <span className="block text-label text-t3">{entry.module ?? ""}</span>
            </Td>
            <Td className="hidden max-w-64 lg:table-cell">
              <span className="block truncate" title={[entry.actor_kind, entry.actor_id].filter(Boolean).join(" ")}>
                {entry.actor_id ?? "—"}
              </span>
              <span className="block text-label text-t3">{entry.actor_kind}</span>
            </Td>
            <Td className="hidden max-w-64 md:table-cell">
              <span className="block truncate" title={[entry.target_kind, entry.target_id].filter(Boolean).join(" ")}>
                {entry.target_id ?? "—"}
              </span>
              <span className="block text-label text-t3">{entry.target_kind}</span>
            </Td>
            <Td className="hidden max-w-48 xl:table-cell">
              <span className="block truncate font-mono text-label" title={entry.request_id}>
                {entry.request_id ?? "—"}
              </span>
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
