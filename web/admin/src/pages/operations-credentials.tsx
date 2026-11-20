import { type AdminSchemas, ApiError } from "@axiom/api";
import { LoadMore } from "@axiom/app";
import {
  ActionButton,
  Card,
  Cluster,
  DataState,
  EmptyState,
  Id,
  PageHead,
  Stack,
  Table,
  Tag,
  Td,
  Text,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import {
  CreateCredentialDialog,
  CredentialSecretDialog,
  type RevealedSecret,
  RevokeCredentialDialog,
  RotateCredentialDialog,
} from "../operations-credential-dialogs";
import { Actions, Code, Head, PageBody, State, SubLine } from "../parts";
import { operationsCredentialsQuery } from "../queries";

type Credential = AdminSchemas["OperationsCredential"];
type Open =
  | { kind: "create" }
  | { kind: "rotate" | "revoke"; credential: Credential }
  | { kind: "secret"; secret: RevealedSecret }
  | null;

function statusOf(credential: Credential): "active" | "revoked" | "expired" {
  if (credential.revoked_at) {
    return "revoked";
  }
  return Date.parse(credential.expires_at) <= Date.now() ? "expired" : "active";
}

export default function OperationsCredentialsPage() {
  const { t } = useTranslation();
  const query = useInfiniteQuery(operationsCredentialsQuery);
  const [open, setOpen] = useState<Open>(null);
  const rows = query.data?.pages.flatMap((page) => page.data) ?? [];

  if (query.error instanceof ApiError && query.error.status === 404) {
    return (
      <>
        <PageHead title={t("operations.credentials.title")} lede={t("operations.credentials.description")} />
        <PageBody>
          <Card title={t("operations.credentials.unavailableTitle")}>
            <Text tone="muted">{t("operations.credentials.unavailableBody")}</Text>
          </Card>
        </PageBody>
      </>
    );
  }

  return (
    <>
      <PageHead title={t("operations.credentials.title")} lede={t("operations.credentials.description")} />
      <PageBody>
        <Actions>
          <ActionButton variant="pri" icon="plus" onClick={() => setOpen({ kind: "create" })}>
            {t("operations.credentials.new")}
          </ActionButton>
        </Actions>
        <Card layout="flush" title={t("operations.credentials.listTitle")}>
          <DataState
            query={query}
            isEmpty={() => rows.length === 0}
            empty={
              <EmptyState title={t("operations.credentials.emptyTitle")}>
                {t("operations.credentials.emptyBody")}
              </EmptyState>
            }
          >
            {() => <CredentialsTable credentials={rows} onOpen={setOpen} />}
          </DataState>
        </Card>
        <LoadMore list={query} />
      </PageBody>
      <OpenCredentialDialog open={open} setOpen={setOpen} />
    </>
  );
}

function OpenCredentialDialog({ open, setOpen }: { open: Open; setOpen: (next: Open) => void }) {
  const close = () => setOpen(null);
  const reveal = (secret: RevealedSecret) => setOpen({ kind: "secret", secret });
  if (open?.kind === "create") {
    return <CreateCredentialDialog onReveal={reveal} onClose={close} />;
  }
  if (open?.kind === "rotate") {
    return <RotateCredentialDialog credential={open.credential} onReveal={reveal} onClose={close} />;
  }
  if (open?.kind === "revoke") {
    return <RevokeCredentialDialog credential={open.credential} onClose={close} />;
  }
  if (open?.kind === "secret") {
    return <CredentialSecretDialog secret={open.secret} onClose={close} />;
  }
  return null;
}

function CredentialsTable({ credentials, onOpen }: { credentials: Credential[]; onOpen: (open: Open) => void }) {
  const { t } = useTranslation();
  return (
    <Table label={t("operations.credentials.listTitle")}>
      <Head
        cols={[
          [t("operations.credentials.col.name"), { main: true }],
          t("operations.credentials.col.state"),
          [t("operations.credentials.col.scope"), { main: true }],
          t("operations.credentials.col.expiry"),
        ]}
        action
      />
      <tbody>
        {credentials.map((credential) => (
          <CredentialRow key={credential.id} credential={credential} onOpen={onOpen} />
        ))}
      </tbody>
    </Table>
  );
}

function CredentialRow({ credential, onOpen }: { credential: Credential; onOpen: (open: Open) => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  const status = statusOf(credential);
  const tone = status === "active" ? "good" : status === "revoked" ? "bad" : "neutral";
  const siteSummary = credential.sites
    ? t("operations.credentials.siteCount", { count: credential.sites.length })
    : t("operations.credentials.allSites");
  return (
    <tr>
      <Td main>
        <Stack gap={4}>
          <Text as="span">{credential.name}</Text>
          <Cluster gap={4}>
            <Id value={credential.id} />
            <Code>{`…${credential.token_last4}`}</Code>
          </Cluster>
          {credential.previous_token_until ? (
            <SubLine>
              {t("operations.credentials.rotatingUntil", {
                at: format.dateTimeMinute(new Date(credential.previous_token_until)),
              })}
            </SubLine>
          ) : null}
        </Stack>
      </Td>
      <Td>
        <State tone={tone}>{t(`operations.credentials.state.${status}`)}</State>
      </Td>
      <Td main>
        <Stack gap={4}>
          <Cluster gap={4}>
            {credential.platform ? <Tag tone="warn">{t("operations.credentials.platformScope")}</Tag> : null}
            <Tag tone="neutral">{siteSummary}</Tag>
            {credential.permissions.map((perm) => (
              <Tag key={perm} tone="neutral">
                {perm}
              </Tag>
            ))}
          </Cluster>
          <SubLine>{t("operations.credentials.vendors", { list: credential.proposer_vendors.join(", ") })}</SubLine>
        </Stack>
      </Td>
      <Td shrink>
        <Text as="div">{format.dateTimeMinute(new Date(credential.expires_at))}</Text>
        <SubLine>
          {credential.last_used_at
            ? t("operations.credentials.lastUsed", { at: format.dateTimeMinute(new Date(credential.last_used_at)) })
            : t("operations.credentials.neverUsed")}
        </SubLine>
      </Td>
      <Td actions>{status === "active" ? <CredentialActions credential={credential} onOpen={onOpen} /> : null}</Td>
    </tr>
  );
}

function CredentialActions({ credential, onOpen }: { credential: Credential; onOpen: (open: Open) => void }) {
  const { t } = useTranslation();
  return (
    <Cluster justify="end">
      <ActionButton
        size="sm"
        aria-label={t("operations.credentials.rotateNamed", { name: credential.name })}
        onClick={() => onOpen({ kind: "rotate", credential })}
      >
        {t("operations.credentials.rotate")}
      </ActionButton>
      <ActionButton
        size="sm"
        aria-label={t("operations.credentials.revokeNamed", { name: credential.name })}
        onClick={() => onOpen({ kind: "revoke", credential })}
      >
        {t("operations.credentials.revoke")}
      </ActionButton>
    </Cluster>
  );
}

export { default as messages } from "../locales/operations";
