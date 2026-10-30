import { ApiError, type Schemas } from "@axiom/api";
import { LoadMore, readForm, useErrors } from "@axiom/app";
import {
  Badge,
  Button,
  DataState,
  EmptyState,
  Icon,
  PageHeader,
  Panel,
  SelectField,
  Table,
  Td,
  TextField,
  Th,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { memo, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { callsCsv, callsName } from "../calls";
import { CallStatus, toInstant, toLocalInput } from "../parts";
import { type CallFilters, type CallGroupBy, callAggregateQuery, callsQuery } from "../queries";
import { save } from "../statement";

type CallsSearch = CallFilters & { group_by?: CallGroupBy };

export default function Calls() {
  const { t } = useTranslation();
  const search: CallsSearch = useSearch({ from: "/app/calls" });
  const { group_by, ...filters } = search;
  const groupBy: CallGroupBy = group_by ?? "day";
  const navigate = useNavigate();
  const calls = useInfiniteQuery(callsQuery(filters));
  const report = useQuery(callAggregateQuery(filters, groupBy));
  const reportsUnavailable = report.error instanceof ApiError && report.error.status === 404;
  const filtered = Object.values(filters).some(Boolean);
  const rows = useMemo(() => calls.data?.pages.flatMap((page) => page.data) ?? [], [calls.data]);
  return (
    <>
      <PageHeader title={t("nav.calls")} description={t("calls.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        <Filters filters={filters} />
        <Summary
          filters={filters}
          groupBy={groupBy}
          onGroupBy={(next) => void navigate({ to: "/calls", search: { ...filters, group_by: next } })}
          unavailable={reportsUnavailable}
        />
        <Panel>
          <DataState query={calls} isEmpty={() => rows.length === 0} empty={<NoCalls filtered={filtered} />}>
            {() => <CallsTable rows={rows} />}
          </DataState>
          <LoadMore list={calls} />
        </Panel>
      </div>
    </>
  );
}

function NoCalls({ filtered }: { filtered: boolean }) {
  const { t } = useTranslation();
  return (
    <EmptyState title={filtered ? t("calls.noMatch") : t("calls.empty.title")}>
      {filtered ? t("calls.noMatchBody") : t("calls.empty.body")}
    </EmptyState>
  );
}

function filtersOf(form: HTMLFormElement): CallFilters {
  const values = readForm(form);
  const status = values.status;
  return {
    model: values.model?.trim() || undefined,
    client_app: values.client_app?.trim() || undefined,
    key_id: values.key_id?.trim() || undefined,
    workspace_id: values.workspace_id?.trim() || undefined,
    status: status === "success" || status === "error" ? status : undefined,
    request_id: values.request_id?.trim() || undefined,
    since: toInstant(values.since),
    until: toInstant(values.until),
  };
}

// The filters live in the address, so a reload, the back button and a link keep them. The form is rebuilt when they
// change from outside it.
function Filters({ filters }: { filters: CallFilters }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  return (
    <Panel className="p-4">
      <form
        key={JSON.stringify(filters)}
        noValidate
        className="grid gap-4 sm:grid-cols-2 xl:grid-cols-[repeat(4,minmax(0,1fr))_auto] xl:items-end"
        onSubmit={(event) => {
          event.preventDefault();
          void navigate({ to: "/calls", search: filtersOf(event.currentTarget) });
        }}
      >
        <TextFilterFields filters={filters} />
        <TimeFilterFields filters={filters} />
        <div className="flex gap-2">
          <Button variant="primary" size="md" type="submit">
            {t("calls.filter.apply")}
          </Button>
          <Button size="md" asChild>
            <Link
              to="/calls"
              search={{
                model: undefined,
                client_app: undefined,
                key_id: undefined,
                workspace_id: undefined,
                status: undefined,
                request_id: undefined,
                since: undefined,
                until: undefined,
                group_by: undefined,
              }}
            >
              {t("calls.filter.reset")}
            </Link>
          </Button>
        </div>
      </form>
    </Panel>
  );
}

function TextFilterFields({ filters }: { filters: CallFilters }) {
  const { t } = useTranslation();
  return (
    <>
      <TextField
        label={t("calls.filter.model")}
        name="model"
        maxLength={200}
        defaultValue={filters.model}
        autoComplete="off"
        spellCheck={false}
      />
      <TextField
        label={t("calls.filter.app")}
        name="client_app"
        maxLength={200}
        defaultValue={filters.client_app}
        autoComplete="off"
      />
      <TextField
        label={t("calls.filter.key")}
        name="key_id"
        maxLength={64}
        defaultValue={filters.key_id}
        placeholder="key_…"
        autoComplete="off"
        spellCheck={false}
      />
      <TextField
        label={t("calls.filter.workspace")}
        name="workspace_id"
        maxLength={64}
        defaultValue={filters.workspace_id}
        placeholder="ws_…"
        autoComplete="off"
        spellCheck={false}
      />
      <SelectField label={t("calls.filter.status")} name="status" defaultValue={filters.status ?? ""}>
        <option value="">{t("calls.filter.statusAll")}</option>
        <option value="success">{t("calls.filter.statusSuccess")}</option>
        <option value="error">{t("calls.filter.statusError")}</option>
      </SelectField>
      <TextField
        label={t("calls.filter.request")}
        name="request_id"
        maxLength={36}
        defaultValue={filters.request_id}
        autoComplete="off"
        spellCheck={false}
      />
    </>
  );
}

function TimeFilterFields({ filters }: { filters: CallFilters }) {
  const { t } = useTranslation();
  return (
    <>
      <TextField
        label={t("calls.filter.since")}
        name="since"
        type="datetime-local"
        defaultValue={toLocalInput(filters.since)}
      />
      <TextField
        label={t("calls.filter.until")}
        name="until"
        type="datetime-local"
        defaultValue={toLocalInput(filters.until)}
      />
    </>
  );
}

// useCallsExport downloads the same filters as the list. A refused or failed request is shown beside the button, not
// replacing the table: the list is still the page's main answer.
function useCallsExport(filters: CallFilters) {
  const [failed, setFailed] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const start = () => {
    setBusy(true);
    setFailed(null);
    callsCsv(filters)
      .then((csv) => save(new File([csv], callsName(), { type: "text/csv" })))
      .catch(setFailed)
      .finally(() => setBusy(false));
  };
  return { failed, busy, start };
}

// Summary is the aggregate of the same filters. It is a separate query so the list stays usable when an older node
// has no report endpoint; the same 404 turns the export off and says why.
function Summary({
  filters,
  groupBy,
  onGroupBy,
  unavailable,
}: {
  filters: CallFilters;
  groupBy: CallGroupBy;
  onGroupBy: (groupBy: CallGroupBy) => void;
  unavailable: boolean;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const errors = useErrors();
  const report = useQuery(callAggregateQuery(filters, groupBy));
  const exported = useCallsExport(filters);
  return (
    <Panel
      title={t("calls.summary.title")}
      action={
        <SummaryActions
          groupBy={groupBy}
          onGroupBy={onGroupBy}
          unavailable={unavailable}
          busy={exported.busy}
          onExport={exported.start}
        />
      }
    >
      {unavailable ? (
        <EmptyState compact title={t("calls.summary.unavailable")}>
          {t("calls.summary.unavailableBody")}
        </EmptyState>
      ) : (
        <>
          {exported.failed ? (
            <p role="alert" className="border-b border-b1 px-4 py-2 text-ui text-bad [overflow-wrap:anywhere]">
              {t("calls.summary.exportFailed", { reason: errors.message(exported.failed) })}
            </p>
          ) : null}
          {report.data?.since && report.data.until ? (
            <p className="border-b border-b1 px-4 py-2 text-ui text-t3">
              {t("calls.summary.window", {
                since: format.dateTime(new Date(report.data.since)),
                until: format.dateTime(new Date(report.data.until)),
              })}
            </p>
          ) : null}
          <DataState
            query={report}
            isEmpty={(data) => !data.data || data.data.length === 0}
            empty={<EmptyState compact title={t("calls.summary.empty")} />}
          >
            {(data) => <AggregateTable rows={data.data ?? []} />}
          </DataState>
        </>
      )}
      <p className="border-t border-b1 px-4 py-2 text-ui text-t3">{t("calls.summary.note")}</p>
    </Panel>
  );
}

function SummaryActions({
  groupBy,
  onGroupBy,
  unavailable,
  busy,
  onExport,
}: {
  groupBy: CallGroupBy;
  onGroupBy: (groupBy: CallGroupBy) => void;
  unavailable: boolean;
  busy: boolean;
  onExport: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-wrap items-center justify-end gap-2">
      <SelectField
        label={t("calls.summary.groupBy")}
        value={groupBy}
        onChange={(event) => onGroupBy(event.currentTarget.value as CallGroupBy)}
        className="min-w-36"
      >
        <option value="day">{t("calls.summary.group.day")}</option>
        <option value="model">{t("calls.summary.group.model")}</option>
        <option value="key">{t("calls.summary.group.key")}</option>
      </SelectField>
      <Button size="sm" onClick={onExport} disabled={busy || unavailable} aria-busy={busy}>
        {busy ? <Icon name="refresh" className="animate-spin" /> : null}
        {t("calls.summary.export")}
      </Button>
    </div>
  );
}

function AggregateTable({ rows }: { rows: Schemas["CallAggregate"][] }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Table label={t("calls.summary.title")}>
      <thead>
        <tr>
          <Th>{t("calls.summary.bucket")}</Th>
          <Th className="text-right">{t("calls.summary.calls")}</Th>
          <Th className="text-right">{t("calls.summary.price")}</Th>
        </tr>
      </thead>
      <tbody>
        {rows.map((row, i) => (
          <tr key={`${row.bucket ?? ""}:${i}`} className="hover:bg-s2">
            <Td className="[overflow-wrap:anywhere]">{row.bucket ?? "—"}</Td>
            <Td className="text-right tabular-nums">{row.calls ?? "—"}</Td>
            <Td className="text-right whitespace-nowrap tabular-nums">
              {row.price?.amount && row.price.currency
                ? format.money({ amount: row.price.amount, currency: row.price.currency })
                : "—"}
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

function CallsTable({ rows }: { rows: Schemas["Call"][] }) {
  const { t } = useTranslation();
  return (
    <Table label={t("nav.calls")}>
      <thead>
        <tr>
          <Th>{t("calls.col.time")}</Th>
          <Th className="hidden sm:table-cell">{t("calls.col.model")}</Th>
          <Th className="hidden 2xl:table-cell">{t("calls.col.app")}</Th>
          <Th>{t("calls.col.status")}</Th>
          <Th className="hidden text-right md:table-cell">{t("calls.col.tokens")}</Th>
          <Th className="hidden text-right lg:table-cell">{t("calls.col.latency")}</Th>
          <Th className="text-right">{t("calls.col.price")}</Th>
        </tr>
      </thead>
      <tbody>
        {rows.map((call) => (
          <CallRow key={call.id} call={call} />
        ))}
      </tbody>
    </Table>
  );
}

const CallRow = memo(function CallRow({ call }: { call: Schemas["Call"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <tr className="hover:bg-s2">
      <Td className="sm:whitespace-nowrap">
        <Link to="/calls/$callId" params={{ callId: call.id }} className="text-t1 underline-offset-4 hover:underline">
          {format.dateTime(new Date(call.at))}
        </Link>
        <p className="mt-0.5 max-w-36 truncate text-label text-t3 sm:hidden">{call.model}</p>
      </Td>
      <Td className="hidden max-w-64 sm:table-cell">
        <p className="truncate" title={call.model}>
          {call.model}
        </p>
      </Td>
      <Td className="hidden max-w-40 2xl:table-cell">
        <p className="truncate" title={call.client_app}>
          {call.client_app || "—"}
        </p>
      </Td>
      <Td>
        <CallStatus errorKind={call.error_kind} compact />
      </Td>
      <Td className="hidden text-right whitespace-nowrap tabular-nums md:table-cell">
        {t("calls.tokens", {
          input: format.number(call.usage.input ?? 0),
          output: format.number(call.usage.output ?? 0),
        })}
      </Td>
      <Td className="hidden text-right whitespace-nowrap tabular-nums lg:table-cell">
        {t("calls.ms", { value: format.number(call.latency_ms) })}
      </Td>
      <Td className="min-w-28 text-right tabular-nums [overflow-wrap:anywhere] sm:whitespace-nowrap">
        {call.free ? <Badge tone="ok">{t("calls.free")}</Badge> : format.money(call.price)}
        <p className="mt-1 max-w-44 truncate text-label text-t3" title={call.price_version}>
          {call.price_version
            ? t("calls.priceVersion", { version: call.price_version })
            : t("calls.priceVersionUnavailable")}
        </p>
      </Td>
    </tr>
  );
});
