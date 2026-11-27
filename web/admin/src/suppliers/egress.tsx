import { type AdminSchemas, adminApi, unwrap, unwrapChange } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { Select } from "@axiom/ui";
import { queryOptions, useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import { retry } from "../monitor-queries";

type Credential = AdminSchemas["SupplierCredential"];
type Supplier = AdminSchemas["Supplier"];
type Proxy = AdminSchemas["EgressProxy"];
type T = TFunction<"admin">;

// The exit a key's calls and checks leave by (suppliers.md「出口代理」): direct, or one of the platform's egress proxies.
// The proxies themselves are added through the API; a page that lists none offers no choice and keys go direct.

export const egressProxiesQuery = queryOptions({
  queryKey: [accountKey, "egress-proxies"],
  queryFn: ({ signal }) => unwrap(adminApi.GET("/api/admin/v1/egress-proxies", { signal })),
  retry,
});

// exitName is how a key's exit reads: the proxy's name, or direct.
export function exitName(t: T, proxies: readonly Proxy[], id: string | undefined) {
  if (!id) return t("upstream.egress.direct");
  const proxy = proxies.find((p) => p.id === id);
  return proxy ? t("upstream.egress.option", { name: proxy.name, id }) : id;
}

// EgressSelect is the egress_id field of a form: shown only when there is a proxy to choose.
export function EgressSelect({ defaultValue = "" }: { defaultValue?: string }) {
  const { t } = useTranslation();
  const proxies = useQuery(egressProxiesQuery).data?.data ?? [];
  if (proxies.length === 0) return null;
  return (
    <Select
      label={t("upstream.egress.label")}
      hint={t("upstream.egress.hint")}
      name="egress_id"
      defaultValue={defaultValue}
    >
      <option value="">{t("upstream.egress.direct")}</option>
      {proxies.map((p) => (
        <option key={p.id} value={p.id}>
          {t(p.status === "active" ? "upstream.egress.option" : "upstream.egress.disabled", { name: p.name, id: p.id })}
        </option>
      ))}
    </Select>
  );
}

// CredentialEgressDialog sets one key's exit.
export function CredentialEgressDialog({
  supplier,
  credential,
  onClose,
}: {
  supplier: Supplier;
  credential: Credential;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const proxies = useQuery(egressProxiesQuery).data?.data ?? [];
  return (
    <ChangeDialog
      operation={`supplier-credential-egress:${credential.id}`}
      title={t("upstream.egress.title")}
      intro={<p>{t("upstream.egress.body")}</p>}
      action={t("upstream.egress.action")}
      fields={() => <EgressSelect defaultValue={credential.egress_id ?? ""} />}
      read={(values) => values.egress_id || null}
      send={(egress, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/suppliers/{supplier_id}/credentials/{credential_id}/egress", {
            params: {
              path: { supplier_id: supplier.id, credential_id: credential.id },
              header: { "Idempotency-Key": key },
            },
            body: { egress_id: egress, reason },
          }),
        )
      }
      done={(changed) => <p>{t("upstream.egress.done", { exit: exitName(t, proxies, changed.egress_id) })}</p>}
      onClose={onClose}
    />
  );
}
