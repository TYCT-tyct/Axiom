import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import { credentialName, usableCredential } from "./upstream";

type Credential = AdminSchemas["SupplierCredential"];
type Check = AdminSchemas["CredentialCheck"];

const at = (credential: Credential, key: string) => ({
  path: { supplier_id: credential.supplier_id ?? "", credential_id: credential.id },
  header: { "Idempotency-Key": key },
});

// The others on a credential's line that routing can still pick: where its requests go when it stops.
export const matesOf = (credential: Credential, all: readonly Credential[]) =>
  all.filter(
    (other) => other.id !== credential.id && other.group_id === credential.group_id && usableCredential(other),
  );

function setEnabled(credential: Credential, enabled: boolean, reason: string, key: string) {
  return unwrapChange(
    adminApi.PUT("/api/admin/v1/suppliers/{supplier_id}/credentials/{credential_id}/enabled", {
      params: at(credential, key),
      body: { enabled, reason },
    }),
  );
}

// 接请求 (adA_upkOff): a person stops a credential, or starts it again. It says where the requests go when it stops and
// warns when it is the last one its line can use; the toast offers to undo it, which is the same switch back.
export function EnableDialog({
  credential,
  line,
  mates,
  onClose,
}: {
  credential: Credential;
  line: string;
  mates: number;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const enable = credential.status === "disabled";
  const yes = t("upstream.enable.yes");
  const no = t("upstream.enable.no");
  const warn = enable
    ? credential.last_check?.cause === "credential_rejected"
      ? t("upstream.enable.warnRejected")
      : undefined
    : mates > 0
      ? `${credential.in_flight ? t("upstream.enable.warnInFlight", { count: credential.in_flight }) : ""}${t("upstream.enable.warnMates", { line, count: mates })}`
      : t("upstream.enable.warnOnly", { line });
  return (
    <ChangeDialog
      operation={`credential-enabled:${credential.id}:${enable}`}
      title={t(enable ? "upstream.enable.onTitle" : "upstream.enable.offTitle")}
      intro={<p>{`${credentialName(credential)}，${line}`}</p>}
      diff={[{ label: t("upstream.enable.row"), before: enable ? no : yes, after: enable ? yes : no }]}
      warn={warn}
      action={t(enable ? "upstream.enable.on" : "upstream.enable.off")}
      danger={!enable && mates === 0}
      read={() => enable}
      send={(enabled, { reason, key }) => setEnabled(credential, enabled, reason, key)}
      done={() => null}
      toast={() => t(enable ? "upstream.enable.doneOn" : "upstream.enable.doneOff")}
      undo={(_, { reason, key }) => setEnabled(credential, !enable, reason, key)}
      onClose={onClose}
    />
  );
}

// A check in words: whether the supplier took the credential, and why not.
export function checkText(t: TFunction<"admin">, check: Check) {
  const cause = check.cause ? t(`upstream.cause.${check.cause}`) : "";
  return check.outcome === "passed" || check.outcome === "duplicate"
    ? t(`upstream.testOne.${check.outcome}`)
    : t(`upstream.testOne.${check.outcome}`, { cause });
}

// 检测 (adA_upkTest): one request the supplier does not charge for. A refused key becomes rejected and routing stops
// picking it; one that passes is picked again. The result is the toast.
export function TestDialog({ credential, onClose }: { credential: Credential; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`credential-test:${credential.id}`}
      title={t("upstream.testOne.title", { name: credentialName(credential) })}
      intro={<p>{t("upstream.testOne.body")}</p>}
      action={t("upstream.testOne.action")}
      read={() => null}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/suppliers/{supplier_id}/credentials/{credential_id}/tests", {
            params: at(credential, key),
            body: { reason },
          }),
        )
      }
      done={() => null}
      toast={(check) => checkText(t, check)}
      onClose={onClose}
    />
  );
}

// 删除这把 Key (adA_upkDel): the sealed key is cleared for good, so the person types its last four characters first.
export function RemoveCredentialDialog({
  credential,
  line,
  mates,
  onClose,
}: {
  credential: Credential;
  line: string;
  mates: number;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`credential-remove:${credential.id}`}
      title={t("upstream.removeOne.title")}
      intro={<p>{`${credentialName(credential)}，${line}`}</p>}
      warn={
        mates > 0 ? t("upstream.removeOne.warnMates", { count: mates }) : t("upstream.removeOne.warnOnly", { line })
      }
      action={t("upstream.removeOne.action")}
      danger
      confirm={credential.last4 ?? credential.upstream_key_ref.slice(-4)}
      read={() => null}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/suppliers/{supplier_id}/credentials/{credential_id}/removal", {
            params: at(credential, key),
            body: { reason },
          }),
        )
      }
      done={() => null}
      toast={() => t("upstream.removeOne.done")}
      onClose={onClose}
    />
  );
}

// 全部检测 (adA_upkTestAll): every credential that is not stopped, of one supplier or of all, checked once; the toast
// says how many work and how many the supplier refused.
export function TestAllDialog({ supplierId, onClose }: { supplierId?: string; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`credentials-test:${supplierId ?? "all"}`}
      title={t("upstream.testAll.title")}
      intro={<p>{t("upstream.testAll.body")}</p>}
      action={t("upstream.testAll.action")}
      read={() => null}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/supplier-credentials/tests", {
            params: { header: { "Idempotency-Key": key } },
            body: { supplier_id: supplierId, reason },
          }),
        )
      }
      done={() => null}
      toast={(checks) => {
        const count = (outcome: Check["outcome"]) => checks.data.filter((check) => check.outcome === outcome).length;
        return t("upstream.testAll.result", {
          passed: count("passed"),
          failed: count("failed"),
          other: count("inconclusive"),
          skipped: checks.skipped ?? 0,
        });
      }}
      onClose={onClose}
    />
  );
}

// 删除失效的 Key (adA_upkPurge): the keys the supplier refused, as the page read them, erased at once; one a check has
// passed since is kept. The person types 删除 first.
export function PurgeDialog({ rejected, onClose }: { rejected: readonly Credential[]; onClose: () => void }) {
  const { t } = useTranslation();
  const confirm = t("upstream.credential.delete");
  return (
    <ChangeDialog
      operation={`credentials-purge:${rejected.map((credential) => credential.id).join(",")}`}
      title={t("upstream.purge.title")}
      intro={<p>{rejected.map((credential) => credentialName(credential)).join("、")}</p>}
      warn={t("upstream.purge.warn", { count: rejected.length })}
      action={t("upstream.purge.action", { count: rejected.length })}
      danger
      confirm={confirm}
      read={() => null}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/supplier-credentials/invalid-removals", {
            params: { header: { "Idempotency-Key": key } },
            body: { credential_ids: rejected.map((credential) => credential.id), reason },
          }),
        )
      }
      done={() => null}
      toast={(out) =>
        out.kept.length > 0
          ? t("upstream.purge.kept", { count: out.removed.length, kept: out.kept.length })
          : t("upstream.purge.done", { count: out.removed.length })
      }
      onClose={onClose}
    />
  );
}
