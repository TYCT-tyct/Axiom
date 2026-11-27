import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import {
  ActionButton,
  Cluster,
  DataState,
  Dialog,
  Field,
  SegmentedControl,
  Select,
  Stack,
  Text,
  TextLink,
} from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAllSuppliers } from "../all-suppliers";
import { ChangeDialog, isProblem, type Problem } from "../change";
import { ConnectDialog } from "./connect";
import { EgressSelect } from "./egress";
import { Checks, LimitFields, readLimits } from "./key-fields";
import { supplierTypesQuery } from "./queries";
import { choosableFamilies, formLabel, formOf, readFields, type SupplierType } from "./supplier-types";
import { TypeFields } from "./type-fields";
import { ChosenType, TypePicker } from "./type-picker";
import { modeOf } from "./upstream";

type Check = AdminSchemas["CredentialCheck"];
type T = TFunction<"admin">;

const origin = /^https:\/\/[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$/;

// What a way of adding ends in: what each key or region came to, and the supplier, when one was stored.
type Added = { checks: Check[]; supplierId?: string };

// 添加上游账号 (adA_upkNew): first the channel type, from the types the node's registry describes; then a relay is
// connected by signing in to it (connect.tsx, unchanged), and a direct API or a cloud platform takes the fields its
// descriptor asks for, as a new supplier or, for a direct API, as more keys of one connected already. Every key and
// region is checked before it is kept, and what each came to is listed; closing the result opens the supplier made.
export default function AddUpstream({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const types = useQuery(supplierTypesQuery);
  const [type, setType] = useState<SupplierType>();
  const [made, setMade] = useState<string>();
  const close = () => {
    onClose();
    if (made) void navigate({ to: "/suppliers/$supplierId", params: { supplierId: made } });
  };
  if (!type) {
    return (
      <Dialog title={t("upstream.add")} onClose={onClose}>
        <Stack>
          <Text tone="muted">{t("upstream.addDialog.lede")}</Text>
          <DataState query={types} compact>
            {(list) => <TypePicker types={list.data} onPick={setType} />}
          </DataState>
          <Cluster justify="end">
            <ActionButton onClick={onClose}>{t("cancel", { ns: "common" })}</ActionButton>
          </Cluster>
        </Stack>
      </Dialog>
    );
  }
  const header = <ChosenType type={type} onChange={() => setType(undefined)} />;
  if (type.mode === "relay") return <ConnectDialog header={header} onClose={onClose} />;
  return <TypedDialog key={type.kind} type={type} header={header} onMade={setMade} onClose={close} />;
}

function Result({ added }: { added: Added }) {
  const { t } = useTranslation();
  const passed = added.checks.filter((check) => check.outcome === "passed").length;
  return (
    <>
      <Text medium>{passed > 0 ? t("upstream.addDialog.added", { count: passed }) : t("upstream.addDialog.none")}</Text>
      <Checks checks={added.checks} />
      <Text tone="muted">{passed > 0 ? t("upstream.addDialog.next") : t("upstream.addDialog.noneBody")}</Text>
      {added.supplierId && passed > 0 ? (
        <TextLink>
          <Link to="/suppliers/$supplierId" params={{ supplierId: added.supplierId }}>
            {t("upstream.addDialog.open")}
          </Link>
        </TextLink>
      ) : null}
    </>
  );
}

// What the person chose outside the fields' values: the supplier to add keys to ("" for a new one), the price entry
// and the credential's form.
type Choice = { to: string; entry: string; form: string };

type Typed =
  | { to: string; body: Omit<AdminSchemas["SupplierKeysAdd"], "reason"> }
  | { to: null; body: Omit<AdminSchemas["TypedSupplierCreate"], "reason"> };

// A direct API or a cloud platform of the type chosen, drawn from its descriptor (suppliers-direct.md「后台的「添加上游
// 账号」」). A direct API's keys may go to a supplier of the type connected already, with the type's settings.
function TypedDialog({
  type,
  header,
  onMade,
  onClose,
}: {
  type: SupplierType;
  header: ReactNode;
  onMade: (id: string) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const direct = type.mode === "direct";
  const connected = useAllSuppliers(direct).suppliers.filter((s) => modeOf(s) === "direct" && s.kind === type.kind);
  const [choice, setChoice] = useState<Choice>({
    to: "",
    entry: type.entries[0]?.id ?? "",
    form: type.forms[0]?.name ?? "",
  });
  const choose = (next: Partial<Choice>) => setChoice((was) => ({ ...was, ...next }));
  return (
    <ChangeDialog
      operation={`supplier-type:${type.kind}`}
      title={t("upstream.add")}
      intro={
        <>
          {header}
          <p>{t("upstream.addDialog.lede")}</p>
        </>
      }
      action={t("upstream.addDialog.action")}
      verdict
      fields={(error) => (
        <>
          {connected.length > 0 ? (
            <Select
              label={t("upstream.addDialog.to")}
              name="to"
              value={choice.to}
              onChange={(event) => choose({ to: event.target.value })}
            >
              {connected.map((supplier) => (
                <option key={supplier.id} value={supplier.id}>
                  {supplier.name}
                </option>
              ))}
              <option value="">{t("upstream.addDialog.newApi")}</option>
            </Select>
          ) : null}
          {choice.to === "" ? (
            <NewFields type={type} choice={choice} onChoose={choose} error={error} />
          ) : (
            <>
              <TypeFields fields={keysAndSettings(type)} error={error} />
              <LimitFields error={error} />
            </>
          )}
        </>
      )}
      read={(values) => readTyped(type, choice, values, t)}
      send={(input, { reason, key }) => sendTyped(type, input, reason, key, onMade)}
      done={(added) => <Result added={added} />}
      onClose={onClose}
    />
  );
}

// What more keys of a direct API connected already take: the keys, and the type's settings for them.
const keysAndSettings = (type: SupplierType) =>
  [...(formOf(type)?.fields ?? []), ...type.fields].filter((field) => field.kind === "keys" || field.kind === "text");

const askedOf = (type: SupplierType, form: string) => [...(formOf(type, form)?.fields ?? []), ...type.fields];

type ChoiceProps = {
  type: SupplierType;
  choice: Choice;
  onChoose: (next: Partial<Choice>) => void;
  error: unknown;
};

// A new supplier's fields, in the descriptor's order: where it is connected, the credential's form and fields, the
// type's fields, the limits and the name.
function NewFields(props: ChoiceProps) {
  const { type, choice, onChoose, error } = props;
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <WhereFields {...props} />
      {type.forms.length > 1 ? (
        <SegmentedControl
          label={t("upstream.types.form")}
          value={choice.form}
          options={type.forms.map((form) => ({ value: form.name, label: formLabel(t, form.name) }))}
          onChange={(form) => onChoose({ form })}
        />
      ) : null}
      <TypeFields key={choice.form} fields={askedOf(type, choice.form)} error={error} />
      <LimitFields error={error} />
      <EgressSelect />
      <Field
        label={t("upstream.addDialog.name")}
        placeholder={t("upstream.addDialog.namePlaceholder")}
        name="name"
        defaultValue={type.entries.length > 0 ? type.name : ""}
        maxLength={200}
        autoComplete="off"
        error={errors.field(error, "name")}
      />
    </>
  );
}

// An entry as the choice of entries shows it: a vendor whose account, not the address, decides the price list names the
// account the entry is for.
function entryLabel(t: T, e: SupplierType["entries"][number]): string {
  const where = { origin: e.origin, currency: e.currency };
  return e.account === undefined
    ? t("upstream.types.entryOption", where)
    : t("upstream.types.entryAccountOption", { ...where, account: e.account });
}

// Where a new supplier is connected: a choice of price entry when the type has several, or, for a type without
// entries, the address, the currency and (for a direct API) the family a person gives.
function WhereFields({ type, choice, onChoose, error }: ChoiceProps) {
  const { t } = useTranslation();
  const errors = useErrors();
  const entry = type.entries.find((e) => e.id === choice.entry);
  const families = choosableFamilies(type);
  return (
    <>
      {type.entries.length > 1 ? (
        <Select
          label={t("upstream.types.entry")}
          hint={entry?.models === undefined ? undefined : t("upstream.types.entryModels", { count: entry.models })}
          name="entry"
          value={choice.entry}
          onChange={(event) => onChoose({ entry: event.target.value })}
          options={type.entries.map((e) => ({ value: e.id, label: entryLabel(t, e) }))}
        />
      ) : null}
      {type.entries.length === 0 ? (
        <>
          <Field
            label={t("upstream.addDialog.origin")}
            hint={t("upstream.addDialog.originHint")}
            name="origin"
            type="url"
            inputMode="url"
            placeholder="https://api.example.com"
            autoComplete="off"
            spellCheck={false}
            maxLength={255}
            error={errors.field(error, "origin")}
          />
          {type.mode === "direct" && families.length > 0 ? (
            <Select
              label={t("upstream.addDialog.format")}
              name="format"
              defaultValue={families[0]}
              options={families.map((value) => ({ value, label: t(`upstream.addDialog.formats.${value}`) }))}
            />
          ) : null}
          <Field
            label={t("upstream.addDialog.currency")}
            hint={t("upstream.addDialog.currencyHint")}
            name="settlement_currency"
            defaultValue="USD"
            maxLength={3}
            autoComplete="off"
            spellCheck={false}
            autoCapitalize="characters"
            error={errors.field(error, "settlement_currency")}
          />
        </>
      ) : null}
    </>
  );
}

function readTyped(type: SupplierType, choice: Choice, values: Record<string, string>, t: T): Typed | Problem {
  const limits = readLimits(values, t);
  if (isProblem(limits)) return limits;
  if (choice.to !== "") {
    const read = readFields(keysAndSettings(type), values, t);
    if (isProblem(read)) return read;
    return { to: choice.to, body: { keys: read.keys ?? [], fields: read.fields, ...limits } };
  }
  const where = readWhere(type, choice, values, t);
  if (isProblem(where)) return where;
  const read = readFields(askedOf(type, choice.form), values, t);
  if (isProblem(read)) return read;
  const name = (values.name ?? "").trim();
  if (!name) return { problem: t("upstream.addDialog.problems.name") };
  const form = type.forms.length > 1 ? choice.form : undefined;
  const egress_id = values.egress_id || undefined;
  return { to: null, body: { name, ...where, form, ...read, ...limits, egress_id } };
}

// Where a new supplier is connected: the entry chosen, or for a type without entries the address, the currency and,
// for a direct API, the family a person chooses.
function readWhere(type: SupplierType, choice: Choice, values: Record<string, string>, t: T) {
  if (type.entries.length > 0) return { entry: choice.entry };
  const at = (values.origin ?? "").trim().replace(/\/+$/, "");
  const currency = (values.settlement_currency ?? "").trim().toUpperCase();
  if (!origin.test(at)) return { problem: t("upstream.addDialog.problems.origin") };
  if (!/^[A-Z]{3}$/.test(currency)) return { problem: t("upstream.addDialog.problems.currency") };
  const families = choosableFamilies(type);
  const format = type.mode === "direct" ? (families.find((f) => f === values.format) ?? families[0]) : undefined;
  return { origin: at, settlement_currency: currency, format };
}

async function sendTyped(type: SupplierType, input: Typed, reason: string, key: string, onMade: (id: string) => void) {
  const header = { "Idempotency-Key": key };
  if (input.to !== null) {
    const sent = await unwrapChange(
      adminApi.POST("/api/admin/v1/suppliers/{supplier_id}/keys", {
        params: { path: { supplier_id: input.to }, header },
        body: { ...input.body, reason },
      }),
    );
    return { ...sent, data: { checks: sent.data.data, supplierId: input.to } };
  }
  const sent = await unwrapChange(
    adminApi.POST("/api/admin/v1/supplier-types/{kind}/suppliers", {
      params: { path: { kind: type.kind }, header },
      body: { ...input.body, reason },
    }),
  );
  if (sent.data.supplier) onMade(sent.data.supplier.id);
  return { ...sent, data: { checks: sent.data.checks, supplierId: sent.data.supplier?.id } };
}
