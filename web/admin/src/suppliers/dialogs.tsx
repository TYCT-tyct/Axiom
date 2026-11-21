import { type AdminSchemas, adminApi, unwrap, unwrapChange } from "@axiom/api";
import { accountKey, useErrors } from "@axiom/app";
import { DataState, Field, Select, Text } from "@axiom/ui";
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
          <Text>{t("suppliers.remove.started")}</Text>
          <Text size="sm" tone="faint">
            {t("suppliers.remove.keys")}
          </Text>
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

// The precision an endpoint serves at, which only a person records: no reference system reports it. A request's
// quantization filter matches only a level recorded here; none recorded is not the level unknown.
export function QuantizationDialog({
  supplier,
  endpoint,
  onClose,
}: {
  supplier: Supplier;
  endpoint: AdminSchemas["SupplierEndpoint"];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  type Level = NonNullable<AdminSchemas["SupplierEndpoint"]["quantization"]>;
  const levels = [
    "int4",
    "int8",
    "fp4",
    "mxfp4",
    "nvfp4",
    "fp6",
    "fp8",
    "mxfp8",
    "fp16",
    "bf16",
    "fp32",
    "unknown",
  ] as const satisfies readonly Level[];
  return (
    <ChangeDialog
      operation={`supplier-endpoint-quantization:${endpoint.id}`}
      title={t("suppliers.quantization.title", { name: endpoint.upstream_name })}
      intro={<p>{t("suppliers.quantization.body")}</p>}
      action={t("suppliers.quantization.action")}
      fields={() => (
        <Select
          label={t("suppliers.quantization.precision")}
          hint={t("suppliers.quantization.body")}
          name="quantization"
          defaultValue={endpoint.quantization ?? ""}
        >
          <option value="">{t("suppliers.quantization.none")}</option>
          {levels.map((level) => (
            <option key={level} value={level}>
              {level}
            </option>
          ))}
        </Select>
      )}
      read={(values) => values.quantization ?? ""}
      send={(level, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/suppliers/{supplier_id}/endpoints/{endpoint_id}/quantization", {
            params: {
              path: { supplier_id: supplier.id, endpoint_id: endpoint.id },
              header: { "Idempotency-Key": key },
            },
            body: { quantization: level === "" ? null : (level as Level), reason },
          }),
        )
      }
      done={(changed) => (
        <p>
          {changed.quantization
            ? t("suppliers.quantization.done", { level: changed.quantization })
            : t("suppliers.quantization.cleared")}
        </p>
      )}
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
        <Field
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

// Binding is the exception: the sync makes the keys a group needs, and this binds one that already exists at the
// supplier to a group.
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
        <Select label={t("suppliers.bind.group")} name="group_id" defaultValue={guess?.id ?? ""}>
          <option value="">{t("suppliers.bind.choose")}</option>
          {groups.map((group) => (
            <option key={group.id} value={group.id}>
              {group.codename ?? group.name}
            </option>
          ))}
        </Select>
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

// Turning an endpoint off removes it from routing; turning it back on also resets its circuit breaker.
export function EndpointEnabledDialog({
  supplier,
  endpoint,
  group,
  onClose,
}: {
  supplier: Supplier;
  endpoint: AdminSchemas["SupplierEndpoint"];
  group: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const next = !endpoint.enabled;
  const mode = next ? "enable" : "disable";
  return (
    <ChangeDialog
      operation={`supplier-endpoint-enabled:${endpoint.id}:${mode}`}
      title={t(`suppliers.endpointEnabled.${mode}.title`, { name: endpoint.upstream_name, group })}
      intro={<p>{t(`suppliers.endpointEnabled.${mode}.body`)}</p>}
      action={t(`suppliers.endpointEnabled.${mode}.action`)}
      danger={!next}
      read={() => next}
      send={(enabled, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/suppliers/{supplier_id}/endpoints/{endpoint_id}/enabled", {
            params: {
              path: { supplier_id: supplier.id, endpoint_id: endpoint.id },
              header: { "Idempotency-Key": key },
            },
            body: { enabled, reason },
          }),
        )
      }
      done={(changed) => (
        <p>{t(changed.enabled ? "suppliers.endpointEnabled.enable.done" : "suppliers.endpointEnabled.disable.done")}</p>
      )}
      onClose={onClose}
    />
  );
}

const maxConcurrencyCap = 2_147_483_647;
const maxRpmCap = 100_000;

function readCredentialLimits(
  values: Record<string, string>,
  words: { concurrency: string; rpm: string },
): { max_concurrency: number; requests_per_minute: number | null } | Problem {
  const rawConcurrency = (values.max_concurrency ?? "").trim();
  const concurrency = Number(rawConcurrency);
  if (!/^\d+$/.test(rawConcurrency) || concurrency < 1 || concurrency > maxConcurrencyCap) {
    return { problem: words.concurrency };
  }
  const rawRpm = (values.requests_per_minute ?? "").trim();
  if (rawRpm === "") {
    return { max_concurrency: concurrency, requests_per_minute: null };
  }
  const rpm = Number(rawRpm);
  if (!/^\d+$/.test(rawRpm) || rpm < 1 || rpm > maxRpmCap) {
    return { problem: words.rpm };
  }
  return { max_concurrency: concurrency, requests_per_minute: rpm };
}

// CredentialLimitsDialog adjusts the in-flight concurrency and optional rolling-minute RPM ceiling of a bound key.
export function CredentialLimitsDialog({
  supplier,
  credential,
  onClose,
}: {
  supplier: Supplier;
  credential: AdminSchemas["SupplierCredential"];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const words = {
    concurrency: t("suppliers.credentialLimits.invalidConcurrency"),
    rpm: t("suppliers.credentialLimits.invalidRpm", { max: maxRpmCap }),
  };
  return (
    <ChangeDialog
      operation={`supplier-credential-limits:${credential.id}`}
      title={t("suppliers.credentialLimits.title", { key: credential.upstream_key_ref })}
      intro={<p>{t("suppliers.credentialLimits.body")}</p>}
      action={t("suppliers.credentialLimits.action")}
      fields={(error) => (
        <>
          <Field
            label={t("suppliers.credentials.concurrency")}
            hint={t("suppliers.credentialLimits.concurrencyHint")}
            name="max_concurrency"
            inputMode="numeric"
            autoComplete="off"
            defaultValue={credential.max_concurrency}
            error={errors.field(error, "max_concurrency")}
          />
          <Field
            label={t("suppliers.credentials.rpm")}
            hint={t("suppliers.credentialLimits.rpmHint", { max: maxRpmCap })}
            name="requests_per_minute"
            inputMode="numeric"
            autoComplete="off"
            defaultValue={credential.requests_per_minute ?? ""}
            error={errors.field(error, "requests_per_minute")}
          />
        </>
      )}
      read={(values) => readCredentialLimits(values, words)}
      send={(limits, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/suppliers/{supplier_id}/credentials/{credential_id}/limits", {
            params: {
              path: { supplier_id: supplier.id, credential_id: credential.id },
              header: { "Idempotency-Key": key },
            },
            body: { ...limits, reason },
          }),
        )
      }
      done={(changed) => (
        <p>
          {changed.requests_per_minute === undefined
            ? t("suppliers.credentialLimits.doneNoRpm", { concurrency: changed.max_concurrency })
            : t("suppliers.credentialLimits.done", {
                concurrency: changed.max_concurrency,
                rpm: changed.requests_per_minute,
              })}
        </p>
      )}
      onClose={onClose}
    />
  );
}
