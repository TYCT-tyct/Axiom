import type { Schemas } from "@axiom/api";
import { LoadMore, readForm } from "@axiom/app";
import {
  Badge,
  Button,
  DataState,
  EmptyState,
  PageHeader,
  Panel,
  Table,
  Td,
  TextField,
  Th,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { memo, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { CallStatus } from "../parts";
import { type CallFilters, callsQuery } from "../queries";

// A time in a datetime-local field is the viewer's own clock; the API takes an instant.
function toLocalInput(instant: string | undefined) {
  const date = new Date(instant ?? Number.NaN);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

function toInstant(local: string | undefined) {
  return local ? new Date(local).toISOString() : undefined;
}

export default function Calls() {
  const { t } = useTranslation();
  const filters: CallFilters = useSearch({ from: "/app/calls" });
  const calls = useInfiniteQuery(callsQuery(filters));
  const filtered = Object.values(filters).some(Boolean);
  const rows = useMemo(() => calls.data?.pages.flatMap((page) => page.data) ?? [], [calls.data]);
  return (
    <>
      <PageHeader title={t("nav.calls")} description={t("calls.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        <Filters filters={filters} />
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
  return {
    model: values.model?.trim() || undefined,
    client_app: values.client_app?.trim() || undefined,
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
        <div className="flex gap-2">
          <Button variant="primary" size="md" type="submit">
            {t("calls.filter.apply")}
          </Button>
          <Button size="md" asChild>
            <Link to="/calls" search={{ model: undefined, client_app: undefined, since: undefined, until: undefined }}>
              {t("calls.filter.reset")}
            </Link>
          </Button>
        </div>
      </form>
    </Panel>
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
      </Td>
    </tr>
  );
});
