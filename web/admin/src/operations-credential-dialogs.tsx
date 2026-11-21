import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import {
  ActionButton,
  CheckField,
  Cluster,
  CodeBlock,
  CopyButton,
  Dialog,
  Field,
  Fieldset,
  Grid,
  Mono,
  Stack,
  Text,
  TextArea,
} from "@axiom/ui";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";
import { toInstant, toLocalInput } from "./parts";

type Credential = AdminSchemas["OperationsCredential"];
type CreateInput = Omit<AdminSchemas["OperationsCredentialCreate"], "reason">;

export type RevealedSecret = {
  credentialId: string;
  token: string;
  kind: "created" | "rotated";
};

// Permissions an operator may grant to an operations agent (besides operations.read, which is always added).
// operations.manage, operations.approve and platform_grants.manage are intentionally omitted because the server refuses them.
const grantablePermissions = [
  "operations.write",
  "operations.money",
  "suppliers.view",
  "suppliers.manage",
  "catalog.view",
  "catalog.manage",
  "pricing.view",
  "pricing.manage",
  "users.view",
  "users.manage",
  "credits.grant",
  "calls.search",
  "calls.inspect",
  "audit.view",
  "sites.manage",
] as const;

const siteIdPattern = /^site_[0-9a-hjkmnp-tv-z]{26}$/;
const vendorPattern = /^[a-z0-9][a-z0-9.-]{0,39}$/;

const splitList = (raw: string | undefined) =>
  (raw ?? "")
    .split(/[\s,]+/)
    .map((item) => item.trim())
    .filter(Boolean);

type CreateWords = {
  nameInvalid: string;
  sitesInvalid: string;
  vendorsInvalid: string;
  expiresInvalid: string;
};

function readCreateCredential(values: Record<string, string>, words: CreateWords): CreateInput | Problem {
  const name = (values.name ?? "").trim();
  if (!name || name.length > 100) {
    return { problem: words.nameInvalid };
  }
  const sites = splitList(values.sites);
  if (sites.length > 100 || sites.some((id) => !siteIdPattern.test(id))) {
    return { problem: words.sitesInvalid };
  }
  const proposer_vendors = splitList(values.proposer_vendors);
  if (
    proposer_vendors.length === 0 ||
    proposer_vendors.length > 20 ||
    proposer_vendors.some((v) => !vendorPattern.test(v))
  ) {
    return { problem: words.vendorsInvalid };
  }
  const expires_at = toInstant(values.expires_at);
  if (!expires_at || Date.parse(expires_at) <= Date.now()) {
    return { problem: words.expiresInvalid };
  }
  const permissions = grantablePermissions.filter((perm) => values[`perm:${perm}`] === "on");
  return {
    name,
    permissions,
    ...(sites.length > 0 ? { sites } : {}),
    platform: values.platform === "on",
    proposer_vendors,
    expires_at,
  };
}

// Hands a freshly issued plaintext token directly to the non-dismissible secret dialog once the write has succeeded.
function RevealToken({
  out,
  kind,
  onReveal,
}: {
  out: AdminSchemas["OperationsCredentialToken"];
  kind: "created" | "rotated";
  onReveal: (secret: RevealedSecret) => void;
}) {
  const { t } = useTranslation();
  useEffect(() => {
    if (out.token) {
      onReveal({ credentialId: out.credential_id, token: out.token, kind });
    }
  }, [out, kind, onReveal]);
  return (
    <>
      <Text>{t("operations.credentials.replayedNoToken")}</Text>
      <Text size="sm" tone="faint">
        {t("operations.credentials.idLabel")}
        {": "}
        <Mono>{out.credential_id}</Mono>
      </Text>
    </>
  );
}

export function CreateCredentialDialog({
  onReveal,
  onClose,
}: {
  onReveal: (secret: RevealedSecret) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const words: CreateWords = {
    nameInvalid: t("operations.credentials.nameInvalid"),
    sitesInvalid: t("operations.credentials.sitesInvalid"),
    vendorsInvalid: t("operations.credentials.proposerVendorsInvalid"),
    expiresInvalid: t("operations.credentials.expiresAtInvalid"),
  };
  return (
    <ChangeDialog
      operation="operations-credential:new"
      title={t("operations.credentials.createTitle")}
      intro={<p>{t("operations.credentials.createIntro")}</p>}
      action={t("operations.credentials.createAction")}
      fields={(error) => <CreateCredentialFields error={error} />}
      read={(values) => readCreateCredential(values, words)}
      send={(input, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/operations/credentials", {
            params: { header: { "Idempotency-Key": key } },
            body: { ...input, reason },
          }),
        )
      }
      done={(out) => <RevealToken out={out} kind="created" onReveal={onReveal} />}
      onClose={onClose}
    />
  );
}

function CreateCredentialFields({ error }: { error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const defaultExpiry = toLocalInput(new Date(Date.now() + 30 * 86_400_000).toISOString());
  return (
    <>
      <Field
        label={t("operations.credentials.name")}
        hint={t("operations.credentials.nameHint")}
        name="name"
        maxLength={100}
        autoComplete="off"
        error={errors.field(error, "name")}
      />
      <Fieldset legend={t("operations.credentials.permissions")} hint={t("operations.credentials.permissionsHint")}>
        <Grid columns={2}>
          {grantablePermissions.map((perm) => (
            <CheckField key={perm} name={`perm:${perm}`} label={perm} />
          ))}
        </Grid>
      </Fieldset>
      <CheckField name="platform" label={t("operations.credentials.platformLabel")} />
      <TextArea
        label={t("operations.credentials.sites")}
        hint={t("operations.credentials.sitesHint")}
        name="sites"
        rows={2}
        error={errors.field(error, "sites")}
      />
      <Field
        label={t("operations.credentials.proposerVendors")}
        hint={t("operations.credentials.proposerVendorsHint")}
        name="proposer_vendors"
        defaultValue="deepseek"
        autoComplete="off"
        error={errors.field(error, "proposer_vendors")}
      />
      <Field
        label={t("operations.credentials.expiresAt")}
        hint={t("operations.credentials.expiresAtHint")}
        name="expires_at"
        type="datetime-local"
        defaultValue={defaultExpiry}
        error={errors.field(error, "expires_at")}
      />
    </>
  );
}

export function RotateCredentialDialog({
  credential,
  onReveal,
  onClose,
}: {
  credential: Credential;
  onReveal: (secret: RevealedSecret) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <ChangeDialog
      operation={`operations-credential-rotate:${credential.id}`}
      title={t("operations.credentials.rotateTitle", { name: credential.name })}
      intro={<p>{t("operations.credentials.rotateIntro")}</p>}
      action={t("operations.credentials.rotateAction")}
      fields={(error) => (
        <Field
          label={t("operations.credentials.overlapHours")}
          hint={t("operations.credentials.overlapHint")}
          name="overlap_hours"
          defaultValue="24"
          inputMode="numeric"
          autoComplete="off"
          error={errors.field(error, "overlap_hours")}
        />
      )}
      read={(values): { overlap_hours: number } | Problem => {
        const raw = (values.overlap_hours ?? "").trim();
        const hours = raw === "" ? 24 : Number(raw);
        const valid = (raw === "" || /^\d+$/.test(raw)) && Number.isInteger(hours) && hours >= 0 && hours <= 24;
        return valid ? { overlap_hours: hours } : { problem: t("operations.credentials.overlapInvalid") };
      }}
      send={({ overlap_hours }, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/operations/credentials/{credential_id}/rotate", {
            params: { path: { credential_id: credential.id }, header: { "Idempotency-Key": key } },
            body: { overlap_hours, reason },
          }),
        )
      }
      done={(out) => <RevealToken out={out} kind="rotated" onReveal={onReveal} />}
      onClose={onClose}
    />
  );
}

export function RevokeCredentialDialog({ credential, onClose }: { credential: Credential; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`operations-credential-revoke:${credential.id}`}
      title={t("operations.credentials.revokeTitle", { name: credential.name })}
      intro={<p>{t("operations.credentials.revokeIntro")}</p>}
      action={t("operations.credentials.revokeAction")}
      danger
      confirm={credential.name}
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/operations/credentials/{credential_id}/revoke", {
            params: { path: { credential_id: credential.id }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={(out) => <Text>{t("operations.credentials.revokedDone", { id: out.credential_id })}</Text>}
      onClose={onClose}
    />
  );
}

// Shown once for a freshly created or rotated token. Escape and backdrop clicks do not dismiss it so the operator cannot
// accidentally lose the only copy of the token.
export function CredentialSecretDialog({ secret, onClose }: { secret: RevealedSecret; onClose: () => void }) {
  const { t } = useTranslation();
  const title = t(secret.kind === "created" ? "operations.secret.createdTitle" : "operations.secret.rotatedTitle");
  return (
    <Dialog title={title} onClose={onClose} dismissible={false}>
      <Stack>
        <Text tone="muted">{t("operations.secret.body")}</Text>
        <CodeBlock>{secret.token}</CodeBlock>
        <Cluster justify="between">
          <CopyButton text={secret.token} label={t("operations.secret.copy")} />
          <ActionButton variant="pri" onClick={onClose}>
            {t("operations.secret.saved")}
          </ActionButton>
        </Cluster>
        <Text size="sm" tone="faint">
          {t("operations.credentials.idLabel")}
          {": "}
          <Mono>{secret.credentialId}</Mono>
        </Text>
      </Stack>
    </Dialog>
  );
}
