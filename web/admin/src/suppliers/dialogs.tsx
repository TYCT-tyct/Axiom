import { type AdminSchemas, adminApi, unwrap, unwrapChange } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { DataState, SelectField, TextField } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "../change";
import { Code } from "../parts";
import { LoginFields, readLogin } from "./login-fields";

type Supplier = AdminSchemas["Supplier"];
type Group = AdminSchemas["SupplierGroup"];
type Params = (
  supplierId: string,
  key: string,
) => { path: { supplier_id: string }; header: { "Idempotency-Key": string } };

const params: Params = (supplier_id, key) => ({ path: { supplier_id }, header: { "Idempotency-Key": key } });

// Pause takes a supplier out of routing without touching its login; resume puts it back. Both leave it where it was.
export function StatusDialog({
  supplier,
  to,
  onClose,
}: {
  supplier: Supplier;
  to: "active" | "paused";
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`supplier-status:${supplier.id}:${to}`}
      title={t(`suppliers.status.${to}.title`, { name: supplier.name })}
      intro={<p>{t(`suppliers.status.${to}.body`)}</p>}
      action={t(`suppliers.status.${to}.action`)}
      read={() => to}
      send={(status, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/suppliers/{supplier_id}/status", {
            params: params(supplier.id, key),
            body: { status, reason },
          }),
        )
      }
      done={(changed) => <p>{t("suppliers.status.now", { status: t(`suppliers.state.${changed.status}`) })}</p>}
      onClose={onClose}
    />
  );
}

// Removing is for good: the supplier leaves routing, its calls in flight finish, its charge records are read to the
// end and its login is deleted. The keys Axiom made at the supplier stay there for a person to revoke.
export function RemoveDialog({ supplier, onClose }: { supplier: Supplier; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`supplier-remove:${supplier.id}`}
      title={t("suppliers.remove.title", { name: supplier.name })}
      intro={<p>{t("suppliers.remove.body")}</p>}
      action={t("suppliers.remove.action")}
      danger
      confirm={supplier.name}
      read={() => null}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/suppliers/{supplier_id}/removal", {
            params: params(supplier.id, key),
            body: { reason },
          }),
        )
      }
      done={() => (
        <>
          <p>{t("suppliers.remove.started")}</p>
          <p className="text-label text-t3">{t("suppliers.remove.keys")}</p>
        </>
      )}
      onClose={onClose}
    />
  );
}

// The fields a new login needs depend on the kind of system, which the server tells by probing the address again.
export function ReplaceLoginDialog({ supplier, onClose }: { supplier: Supplier; onClose: () => void }) {
  const { t } = useTranslation();
  const detection = useQuery({
    queryKey: [accountKey, "detection", supplier.origin],
    queryFn: ({ signal }) =>
      unwrap(adminApi.POST("/api/admin/v1/suppliers/detections", { body: { origin: supplier.origin }, signal })),
    staleTime: 5 * 60_000,
  });
  const fields = detection.data?.login_fields ?? [];
  return (
    <ChangeDialog
      operation={`supplier-login:${supplier.id}`}
      title={t("suppliers.login.title", { name: supplier.name })}
      intro={<p>{t("suppliers.login.body", { account: supplier.upstream_account })}</p>}
      action={t("suppliers.login.action")}
      fields={() => (
        <DataState query={detection} compact>
          {(found) => <LoginFields fields={found.login_fields} />}
        </DataState>
      )}
      read={(values) => readLogin(values, fields, t("suppliers.connect.loginMissing"))}
      send={(login, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/suppliers/{supplier_id}/login", {
            params: params(supplier.id, key),
            body: { login, reason },
          }),
        )
      }
      done={() => <p>{t("suppliers.login.done")}</p>}
      onClose={onClose}
    />
  );
}

// What one unit of the supplier's balance is worth, in the currency Axiom pays it in: the answer that turns its costs
// into money.
export function UnitValueDialog({ supplier, onClose }: { supplier: Supplier; onClose: () => void }) {
  const { t } = useTranslation();
  const read = (values: Record<string, string>): string | Problem => {
    const value = (values.value ?? "").trim();
    const valid = /^(0|[1-9]\d{0,2})(\.\d{1,15})?$/.test(value) && Number(value) > 0 && Number(value) <= 1000;
    return valid ? value : { problem: t("suppliers.unit.invalid") };
  };
  return (
    <ChangeDialog
      operation={`supplier-unit:${supplier.id}`}
      title={t("suppliers.unit.title", { name: supplier.name })}
      intro={
        <p>
          {t("suppliers.unit.body", {
            unit: t(`suppliers.native.${supplier.native_unit}`),
            currency: supplier.settlement_currency,
          })}
        </p>
      }
      action={t("suppliers.unit.action")}
      fields={() => (
        <TextField
          label={t("suppliers.unit.value", { currency: supplier.settlement_currency })}
          hint={
            supplier.unit_value_hint
              ? t("suppliers.unit.hint", { hint: supplier.unit_value_hint })
              : t("suppliers.unit.noHint")
          }
          name="value"
          inputMode="decimal"
          autoComplete="off"
          defaultValue={supplier.unit_value ?? ""}
        />
      )}
      read={read}
      send={(value, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/suppliers/{supplier_id}/unit-value", {
            params: params(supplier.id, key),
            body: { value, reason },
          }),
        )
      }
      done={(changed) => (
        <p>
          {t("suppliers.unit.done", {
            unit: t(`suppliers.native.${changed.native_unit}`),
            value: changed.unit_value,
            currency: changed.settlement_currency,
          })}
        </p>
      )}
      onClose={onClose}
    />
  );
}

// A selected group gets its code name, the name users see in place of the group's, and its models become endpoints.
export function SelectGroupDialog({
  supplier,
  group,
  onClose,
}: {
  supplier: Supplier;
  group: Group;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const selected = !group.selected;
  const way = selected ? "select" : "unselect";
  return (
    <ChangeDialog
      operation={`supplier-group:${group.id}:${way}`}
      title={t(`suppliers.group.${way}.title`, { name: group.name })}
      intro={<p>{t(`suppliers.group.${way}.body`)}</p>}
      action={t(`suppliers.group.${way}.action`)}
      read={() => selected}
      send={(value, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/suppliers/{supplier_id}/groups/{group_id}", {
            params: { path: { supplier_id: supplier.id, group_id: group.id }, header: { "Idempotency-Key": key } },
            body: { selected: value, reason },
          }),
        )
      }
      done={(changed) =>
        changed.codename ? (
          <p>
            {t("suppliers.group.codename")}
            {": "}
            <Code>{changed.codename}</Code>
          </p>
        ) : null
      }
      onClose={onClose}
    />
  );
}

// Binding says which of the keys the supplier lists Axiom sends a group's calls with. M1 does not make keys at the
// supplier, so the key is one that exists there already.
export function BindKeyDialog({
  supplier,
  upstreamKey,
  groups,
  onClose,
}: {
  supplier: Supplier;
  upstreamKey: AdminSchemas["UpstreamKey"];
  groups: Group[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const guess = groups.find((group) => group.upstream_ref === upstreamKey.group_ref);
  return (
    <ChangeDialog
      operation={`supplier-bind:${supplier.id}:${upstreamKey.ref}`}
      title={t("suppliers.bind.title")}
      intro={
        <p>
          {t("suppliers.bind.body")} <Code>{upstreamKey.ref}</Code>
        </p>
      }
      action={t("suppliers.bind.action")}
      fields={() => (
        <SelectField label={t("suppliers.bind.group")} name="group_id" defaultValue={guess?.id ?? ""}>
          <option value="">{t("suppliers.bind.choose")}</option>
          {groups.map((group) => (
            <option key={group.id} value={group.id}>
              {group.codename ?? group.name}
            </option>
          ))}
        </SelectField>
      )}
      read={(values) =>
        values.group_id ? (values.group_id as AdminSchemas["Uuid"]) : { problem: t("suppliers.bind.missing") }
      }
      send={(group_id, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/suppliers/{supplier_id}/credentials", {
            params: params(supplier.id, key),
            body: { group_id, upstream_key_ref: upstreamKey.ref, reason },
          }),
        )
      }
      done={() => <p>{t("suppliers.bind.done")}</p>}
      onClose={onClose}
    />
  );
}
