import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { DataState, Field, SegmentedControl, Select, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, isProblem, type Problem } from "../change";
import { Code } from "../parts";
import { Checks, KeysField, LimitFields, readKeys, readLimits } from "./key-fields";
import { supplierTypesQuery } from "./queries";
import { formLabel, formOf, readFields, type SupplierType } from "./supplier-types";
import { TypeFields } from "./type-fields";
import { modeOf } from "./upstream";

type Supplier = AdminSchemas["Supplier"];
type Group = AdminSchemas["SupplierGroup"];

const key = (k: string) => ({ "Idempotency-Key": k });

// 接入这个分组 (adA_grpOn on a group with no token): the group is taken in and given a token in one change, one the
// system makes at the supplier or one the person made there and pastes, which the supplier must list under this group.
export function ConnectGroupDialog({
  supplier,
  group,
  onClose,
}: {
  supplier: Supplier;
  group: Group;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [source, setSource] = useState<"make" | "paste">("make");
  return (
    <ChangeDialog
      operation={`group-connect:${group.id}`}
      title={t("upstream.connectGroup.title")}
      intro={
        <p>
          <Code>{group.name}</Code>{" "}
          {t("upstream.connectGroup.body", { name: supplier.name, ratio: group.ratio, count: group.model_count ?? 0 })}
        </p>
      }
      action={t("upstream.connectGroup.action")}
      fields={(error) => <TokenFields supplier={supplier} source={source} onSource={setSource} error={error} />}
      read={(values): Omit<AdminSchemas["GroupConnection"], "reason"> | Problem => {
        if (source === "make") return { key_source: "make" };
        const typed = (values.key ?? "").trim();
        return typed.length < 8
          ? { problem: t("upstream.connectGroup.keyMissing") }
          : { key_source: "paste", key: typed };
      }}
      send={(input, { reason, key: k }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/suppliers/{supplier_id}/groups/{group_id}/connection", {
            params: { path: { supplier_id: supplier.id, group_id: group.id }, header: key(k) },
            body: { ...input, reason },
          }),
        )
      }
      done={() => null}
      toast={() => t("upstream.connectGroup.done")}
      onClose={onClose}
    />
  );
}

// A group's token: one the system makes, or one pasted, which the supplier must list under the group.
function TokenFields({
  supplier,
  source,
  onSource,
  error,
}: {
  supplier: Supplier;
  source: "make" | "paste";
  onSource: (source: "make" | "paste") => void;
  error: unknown;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <SegmentedControl
        label={t("upstream.connectGroup.token")}
        value={source}
        options={[
          { value: "make", label: t("upstream.connectGroup.make") },
          { value: "paste", label: t("upstream.connectGroup.paste") },
        ]}
        onChange={onSource}
      />
      <Text size="sm" tone="muted">
        {source === "make"
          ? t("upstream.connectGroup.makeHint", { name: supplier.name })
          : t("upstream.connectGroup.pasteHint")}
      </Text>
      {source === "paste" ? (
        <Field
          label={t("upstream.connectGroup.key")}
          name="key"
          type="password"
          autoComplete="off"
          spellCheck={false}
          maxLength={4096}
          error={errors.field(error, "key")}
        />
      ) : null}
    </>
  );
}

function selectGroup(supplier: Supplier, group: Group, selected: boolean, reason: string, k: string) {
  return unwrapChange(
    adminApi.PUT("/api/admin/v1/suppliers/{supplier_id}/groups/{group_id}", {
      params: { path: { supplier_id: supplier.id, group_id: group.id }, header: key(k) },
      body: { selected, reason },
    }),
  );
}

// 停掉这个分组 and 恢复这个分组 (adA_grpOn on a group already in use): its token stays, so it can come back, and the
// toast offers to undo it.
export function PauseGroupDialog({
  supplier,
  group,
  onClose,
}: {
  supplier: Supplier;
  group: Group;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const stop = group.selected;
  const yes = t("upstream.enable.yes");
  const no = t("upstream.enable.no");
  return (
    <ChangeDialog
      operation={`group-pause:${group.id}:${stop}`}
      title={t(
        stop
          ? "upstream.stopGroup.title"
          : group.codename
            ? "upstream.stopGroup.resumeTitle"
            : "upstream.connectGroup.title",
      )}
      intro={
        <p>
          {supplier.name} <Code>{group.name}</Code>
        </p>
      }
      diff={[{ label: t("upstream.stopGroup.row"), before: stop ? yes : no, after: stop ? no : yes }]}
      warn={stop ? t("upstream.stopGroup.warn") : undefined}
      action={t(stop ? "upstream.stopGroup.stop" : "upstream.stopGroup.resume")}
      read={() => !stop}
      send={(selected, { reason, key: k }) => selectGroup(supplier, group, selected, reason, k)}
      done={() => null}
      toast={() => t(stop ? "upstream.stopGroup.stopped" : "upstream.stopGroup.resumed")}
      undo={(_, { reason, key: k }) => selectGroup(supplier, group, stop, reason, k)}
      onClose={onClose}
    />
  );
}

type Keys = { keys: string[]; group_id?: string; max_concurrency: number; requests_per_minute?: number };

// Keys for a supplier that has its line already (adA_upkNew with a channel): a relay's for one of its groups, each
// checked by the supplier, a direct API's for its one line. The result says what each key came to.
export function AddKeysDialog({
  supplier,
  groups,
  onClose,
}: {
  supplier: Supplier;
  groups: Group[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const relay = modeOf(supplier) === "relay";
  const read = (values: Record<string, string>): Keys | Problem => {
    const keys = readKeys(values, t);
    const limits = readLimits(values, t);
    if (isProblem(keys)) return keys;
    if (isProblem(limits)) return limits;
    if (relay && !values.group_id) return { problem: t("upstream.addDialog.problems.group") };
    return { keys, group_id: relay ? values.group_id : undefined, ...limits };
  };
  return (
    <ChangeDialog
      operation={`supplier-keys:${supplier.id}`}
      title={t(relay ? "upstream.addKeys.relayTitle" : "upstream.addKeys.title", { name: supplier.name })}
      intro={<p>{t("upstream.addDialog.keysHint")}</p>}
      action={t("upstream.addDialog.action")}
      verdict
      fields={(error) => (
        <>
          {relay ? (
            <Select label={t("upstream.addDialog.group")} name="group_id" defaultValue="">
              <option value="">{t("upstream.addDialog.problems.group")}</option>
              {groups.map((group) => (
                <option key={group.id} value={group.id}>
                  {group.name}
                </option>
              ))}
            </Select>
          ) : null}
          <KeysField error={error} />
          <LimitFields error={error} />
        </>
      )}
      read={read}
      send={(body, { reason, key: k }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/suppliers/{supplier_id}/keys", {
            params: { path: { supplier_id: supplier.id }, header: key(k) },
            body: { ...body, reason },
          }),
        )
      }
      done={(out) => <Checks checks={out.data} />}
      onClose={onClose}
    />
  );
}

// 更换凭证 (replaceSupplierCloudCredential): the new credential, in a form of the supplier's type (listSupplierTypes,
// by its kind), is checked in each region given, or in the ones the supplier has now, and takes the old one's place
// only where it passes. Like the first, it is never shown back.
export function ReplaceCloudDialog({ supplier, onClose }: { supplier: Supplier; onClose: () => void }) {
  const { t } = useTranslation();
  const types = useQuery(supplierTypesQuery);
  const type = types.data?.data.find((described) => described.kind === supplier.kind);
  const [form, setForm] = useState<string>();
  const read = (values: Record<string, string>): Omit<AdminSchemas["CloudCredentialReplace"], "reason"> | Problem => {
    if (!type) return { problem: t("upstream.replaceCloud.unknown") };
    const asked = readFields(replaced(type, form), values, t);
    if (isProblem(asked)) return asked;
    const chosen = type.forms.length > 1 ? formOf(type, form)?.name : undefined;
    return { form: chosen, fields: asked.fields, regions: asked.regions };
  };
  return (
    <ChangeDialog
      operation={`cloud-credential:${supplier.id}`}
      title={t("upstream.replaceCloud.title", { name: supplier.name })}
      intro={<p>{t("upstream.replaceCloud.body")}</p>}
      action={t("upstream.replaceCloud.action")}
      verdict
      fields={(error) => (
        <DataState query={types} compact>
          {() =>
            type ? (
              <>
                {type.forms.length > 1 ? (
                  <SegmentedControl
                    label={t("upstream.types.form")}
                    value={formOf(type, form)?.name ?? ""}
                    options={type.forms.map((f) => ({ value: f.name, label: formLabel(t, f.name) }))}
                    onChange={setForm}
                  />
                ) : null}
                <TypeFields
                  key={form}
                  fields={replaced(type, form)}
                  error={error}
                  regionsHint={t("upstream.replaceCloud.regionsHint")}
                />
              </>
            ) : (
              <Text tone="muted">{t("upstream.replaceCloud.unknown")}</Text>
            )
          }
        </DataState>
      )}
      read={read}
      send={(body, { reason, key: k }) => replaceCloud(supplier, { ...body, reason }, k)}
      done={(out) => <Checks checks={out.checks} />}
      onClose={onClose}
    />
  );
}

// What a new credential is asked for: the form's fields and the type's, but not the deployments, which stay, and the
// regions only optionally, since none given keeps the ones the supplier has.
const replaced = (type: SupplierType, form?: string) =>
  [...(formOf(type, form)?.fields ?? []), ...type.fields]
    .filter((field) => field.kind !== "deployments")
    .map((field) => (field.kind === "regions" ? { ...field, required: false } : field));

function replaceCloud(supplier: Supplier, body: AdminSchemas["CloudCredentialReplace"], k: string) {
  return unwrapChange(
    adminApi.PUT("/api/admin/v1/suppliers/{supplier_id}/cloud-credential", {
      params: { path: { supplier_id: supplier.id }, header: key(k) },
      body,
    }),
  );
}
