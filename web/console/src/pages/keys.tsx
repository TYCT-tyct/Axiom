import type { Schemas } from "@axiom/api";
import { can, LoadMore, readForm, useSession } from "@axiom/app";
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
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { CreateKeyDialog, DeleteKeyDialog, EditKeyDialog, SecretDialog } from "../key-dialogs";
import { keyFiltersApplied } from "../key-filters";
import { useNewKeys, usePresetName } from "../key-form";
import { maskedKey, toInstant, toLocalInput } from "../parts";
import { type KeyFilters, keysQuery } from "../queries";

type Key = Schemas["Key"];
type Dialog =
  | { kind: "create" }
  | { kind: "edit"; key: Key }
  | { kind: "delete"; key: Key }
  | { kind: "secret"; created: Schemas["CreatedKey"] };

export default function Keys() {
  const { t } = useTranslation();
  const manage = can(useSession(), "api_keys.manage");
  const filters: KeyFilters = useSearch({ from: "/app/keys" });
  const keys = useInfiniteQuery(keysQuery(filters));
  const filtered = Object.values(filters).some(Boolean);
  const pages = keys.data?.pages ?? [];
  const filtersApplied = keyFiltersApplied(filters, pages);
  const rows = useMemo(() => keys.data?.pages.flatMap((page) => page.data) ?? [], [keys.data]);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const close = () => setDialog(null);
  return (
    <>
      <PageHeader title={t("nav.keys")} description={t("keys.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        {manage ? (
          <Button variant="primary" size="md" onClick={() => setDialog({ kind: "create" })}>
            <Icon name="plus" />
            {t("keys.create")}
          </Button>
        ) : null}
        <Filters filters={filters} />
        <Panel>
          <DataState
            query={keys}
            isEmpty={() => filtersApplied && rows.length === 0}
            empty={<NoKeys filtered={filtered} />}
          >
            {() =>
              filtersApplied ? (
                <KeysTable
                  keys={rows}
                  manage={manage}
                  onEdit={(key) => setDialog({ kind: "edit", key })}
                  onDelete={(key) => setDialog({ kind: "delete", key })}
                />
              ) : (
                <EmptyState title={t("keys.filtersUnavailable.title")}>{t("keys.filtersUnavailable.body")}</EmptyState>
              )
            }
          </DataState>
          {filtersApplied ? <LoadMore list={keys} /> : null}
        </Panel>
      </div>
      {dialog?.kind === "create" ? (
        <CreateKeyDialog onClose={close} onCreated={(created) => setDialog({ kind: "secret", created })} />
      ) : null}
      {dialog?.kind === "edit" ? <EditKeyDialog apiKey={dialog.key} onClose={close} /> : null}
      {dialog?.kind === "delete" ? <DeleteKeyDialog apiKey={dialog.key} onClose={close} /> : null}
      {dialog?.kind === "secret" ? <SecretDialog created={dialog.created} onClose={close} /> : null}
    </>
  );
}

function NoKeys({ filtered }: { filtered: boolean }) {
  const { t } = useTranslation();
  return (
    <EmptyState title={filtered ? t("keys.noMatch") : t("keys.empty.title")}>
      {filtered ? t("keys.noMatchBody") : t("keys.empty.body")}
    </EmptyState>
  );
}

function filtersOf(form: HTMLFormElement): KeyFilters {
  const values = readForm(form);
  return {
    name: values.name?.trim() || undefined,
    prefix: values.prefix?.trim() || undefined,
    status: statusOf(values.status),
    created_after: toInstant(values.created_after),
    created_before: toInstant(values.created_before),
  };
}

function statusOf(value: string | undefined): KeyFilters["status"] {
  return value === "active" || value === "disabled" || value === "expired" ? value : undefined;
}

// The filters live in the address, so a reload, the back button and a link keep them. The form is rebuilt when they
// change from outside it.
function Filters({ filters }: { filters: KeyFilters }) {
  const navigate = useNavigate();
  const form = useRef<HTMLFormElement>(null);
  return (
    <Panel className="p-4">
      <form
        ref={form}
        key={JSON.stringify(filters)}
        noValidate
        className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3"
        onSubmit={(event) => {
          event.preventDefault();
          void navigate({ to: "/keys", search: filtersOf(event.currentTarget) });
        }}
      >
        <KeyFilterFields filters={filters} />
        <KeyFilterActions onClear={() => form.current?.reset()} />
      </form>
    </Panel>
  );
}

function KeyFilterFields({ filters }: { filters: KeyFilters }) {
  const { t } = useTranslation();
  return (
    <>
      <TextField
        label={t("keys.filter.name")}
        name="name"
        maxLength={100}
        defaultValue={filters.name}
        autoComplete="off"
      />
      <TextField
        label={t("keys.filter.prefix")}
        name="prefix"
        maxLength={16}
        defaultValue={filters.prefix}
        autoComplete="off"
        spellCheck={false}
      />
      <SelectField label={t("keys.filter.status")} name="status" defaultValue={filters.status ?? ""}>
        <option value="">{t("keys.filter.anyStatus")}</option>
        <option value="active">{t("keys.enabled")}</option>
        <option value="disabled">{t("keys.disabled")}</option>
        <option value="expired">{t("keys.expired")}</option>
      </SelectField>
      <TextField
        label={t("keys.filter.createdAfter")}
        name="created_after"
        type="datetime-local"
        defaultValue={toLocalInput(filters.created_after)}
      />
      <TextField
        label={t("keys.filter.createdBefore")}
        name="created_before"
        type="datetime-local"
        defaultValue={toLocalInput(filters.created_before)}
      />
    </>
  );
}

function KeyFilterActions({ onClear }: { onClear: () => void }) {
  const { t } = useTranslation();
  return (
    <div className="flex items-end gap-2">
      <Button variant="primary" size="md" type="submit">
        {t("keys.filter.apply")}
      </Button>
      <Button size="md" asChild>
        <Link
          to="/keys"
          search={{
            name: undefined,
            prefix: undefined,
            status: undefined,
            created_after: undefined,
            created_before: undefined,
          }}
          onClick={onClear}
        >
          {t("keys.filter.reset")}
        </Link>
      </Button>
    </div>
  );
}

function KeysTable({
  keys,
  manage,
  onEdit,
  onDelete,
}: {
  keys: Key[];
  manage: boolean;
  onEdit: (key: Key) => void;
  onDelete: (key: Key) => void;
}) {
  const { t } = useTranslation();
  return (
    <Table label={t("nav.keys")}>
      <thead>
        <tr>
          <Th>{t("keys.col.name")}</Th>
          <Th>{t("keys.col.status")}</Th>
          <Th className="hidden md:table-cell">{t("keys.col.limit")}</Th>
          <Th className="hidden xl:table-cell">{t("routingPresets.key.col")}</Th>
          <Th className="hidden lg:table-cell">{t("keys.col.expires")}</Th>
          <Th className="hidden 2xl:table-cell">{t("keys.col.created")}</Th>
          {manage ? <Th className="w-0" /> : null}
        </tr>
      </thead>
      <tbody>
        {keys.map((key) => (
          <KeyRow key={key.id} apiKey={key} manage={manage} onEdit={onEdit} onDelete={onDelete} />
        ))}
      </tbody>
    </Table>
  );
}

function KeyStatus({ apiKey }: { apiKey: Key }) {
  const { t } = useTranslation();
  if (apiKey.expires_at !== undefined && new Date(apiKey.expires_at) <= new Date()) {
    return (
      <Badge tone="bad" icon="alert">
        {t("keys.expired")}
      </Badge>
    );
  }
  return (
    <Badge tone={apiKey.disabled ? "warn" : "ok"} icon={apiKey.disabled ? "alert" : "check"}>
      {apiKey.disabled ? t("keys.disabled") : t("keys.enabled")}
    </Badge>
  );
}

function KeyLimit({ apiKey }: { apiKey: Key }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (!apiKey.spend_limit) {
    return t("keys.noLimit");
  }
  return (
    <>
      <p>{t("keys.limitLeft", { left: apiKey.limit_remaining ? format.money(apiKey.limit_remaining) : "—" })}</p>
      <p className="text-label text-t4">
        {t("keys.limitOf", { limit: format.money(apiKey.spend_limit) })}
        {apiKey.limit_reset ? ` · ${t(`keys.reset.${apiKey.limit_reset}`)}` : ""}
      </p>
    </>
  );
}

// A management key calls no model, so it has no preset. A call key without one routes by the system's allow-all: an
// older key, or a node that gives no preset. A key keeps the preset it was made with when the default moves on
// (docs/domains/routing.md「默认链与 key 落章」), so one bound to another than a key made now takes says so; a key
// without a preset is not compared, since the node that listed it may be one that gives none.
function KeyPreset({ apiKey }: { apiKey: Key }) {
  const { t } = useTranslation();
  const presetName = usePresetName();
  const newKeys = useNewKeys();
  if (apiKey.kind === "management") {
    return <span className="text-t4">{t("routingPresets.key.notApplicable")}</span>;
  }
  if (!apiKey.preset_id) {
    return (
      <span className="text-t4" title={t("routingPresets.key.noneHint")}>
        {t("routingPresets.key.none")}
      </span>
    );
  }
  const name = presetName(apiKey);
  const moved = newKeys.id !== undefined && newKeys.id !== apiKey.preset_id;
  const note = newKeys.name
    ? t("routingPresets.key.notDefaultNamed", { name: newKeys.name })
    : t("routingPresets.key.notDefault");
  return (
    <>
      <p className="truncate" title={name}>
        {name}
      </p>
      {moved ? (
        <p className="truncate text-label text-t4" title={t("routingPresets.key.notDefaultHint")}>
          {note}
        </p>
      ) : null}
    </>
  );
}

function KeyRow({
  apiKey,
  manage,
  onEdit,
  onDelete,
}: {
  apiKey: Key;
  manage: boolean;
  onEdit: (key: Key) => void;
  onDelete: (key: Key) => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const retired = apiKey.retired_models?.join(", ");
  return (
    <tr>
      <Td className="w-full max-w-0 sm:w-auto sm:max-w-64 sm:min-w-40">
        <p className="truncate text-t1" title={apiKey.name}>
          {apiKey.name}
        </p>
        <p className="truncate font-mono text-label text-t4">{maskedKey(apiKey)}</p>
        {retired ? (
          <p className="truncate text-label text-warn" title={t("keys.retiredModels", { models: retired })}>
            {t("keys.retiredModels", { models: retired })}
          </p>
        ) : null}
      </Td>
      <Td>
        <KeyStatus apiKey={apiKey} />
      </Td>
      <Td className="hidden tabular-nums md:table-cell">
        <KeyLimit apiKey={apiKey} />
      </Td>
      <Td className="hidden max-w-48 xl:table-cell">
        <KeyPreset apiKey={apiKey} />
      </Td>
      <Td className="hidden whitespace-nowrap lg:table-cell">
        {apiKey.expires_at ? format.dateTime(new Date(apiKey.expires_at)) : t("keys.neverExpires")}
      </Td>
      <Td className="hidden whitespace-nowrap 2xl:table-cell">{format.dateTime(new Date(apiKey.created_at))}</Td>
      {manage ? (
        <Td className="whitespace-nowrap">
          <Button
            variant="ghost"
            size="icon"
            aria-label={t("keys.editNamed", { name: apiKey.name })}
            onClick={() => onEdit(apiKey)}
          >
            <Icon name="pencil" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            aria-label={t("keys.deleteNamed", { name: apiKey.name })}
            onClick={() => onDelete(apiKey)}
          >
            <Icon name="trash" />
          </Button>
        </Td>
      ) : null}
    </tr>
  );
}
