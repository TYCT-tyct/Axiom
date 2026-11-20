import { consoleApi, type Schemas, unwrap, unwrapEmpty } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { Button, Cluster, CodeBlock, CopyButton, Dialog, FormError, keyGuide, Stack, Text } from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ConfirmDialog } from "./confirm-dialog";
import { KeyForm } from "./key-form";
import { siteQuery } from "./queries";
import { useWorkspaces } from "./workspaces";

type Key = Schemas["Key"];

// The key list and the overview read the same cache entry; a change to a key makes both ask again.
function useRefreshKeys() {
  const queryClient = useQueryClient();
  return () => queryClient.invalidateQueries({ queryKey: [accountKey, "keys"] });
}

// CreateKeyDialog makes a key, in workspace unless the user picks another, and hands on the key with the workspace it
// was asked for, which the secret dialog compares.
export function CreateKeyDialog({
  workspace,
  onClose,
  onCreated,
}: {
  workspace?: string;
  onClose: () => void;
  onCreated: (key: Schemas["CreatedKey"], asked: string | undefined) => void;
}) {
  const { t } = useTranslation();
  const refresh = useRefreshKeys();
  const create = useMutation({
    mutationFn: (body: Schemas["KeyCreate"]) => unwrap(consoleApi.POST("/api/console/v1/keys", { body })),
    onSuccess: async (created, body) => {
      await refresh();
      onCreated(created, body.workspace_id);
    },
  });
  return (
    <Dialog title={t("keys.create")} onClose={onClose}>
      <KeyForm
        workspace={workspace}
        pending={create.isPending}
        error={create.error}
        onCancel={onClose}
        onSubmit={({ disabled: _disabled, ...body }) => create.mutate(body)}
      />
    </Dialog>
  );
}

export function DeleteKeyDialog({ apiKey, onClose }: { apiKey: Key; onClose: () => void }) {
  const { t } = useTranslation();
  const refresh = useRefreshKeys();
  const remove = useMutation({
    mutationFn: () =>
      unwrapEmpty(consoleApi.DELETE("/api/console/v1/keys/{key_id}", { params: { path: { key_id: apiKey.id } } })),
    onSuccess: async () => {
      await refresh();
      onClose();
    },
  });
  return (
    <ConfirmDialog
      title={t("keys.delete.title")}
      body={t("keys.delete.body", { name: apiKey.name })}
      confirm={t("keys.delete.confirm")}
      action={remove}
      onClose={onClose}
    />
  );
}

// One piece of the guide: what it is, the text to copy, and where that text goes.
function Snippet({ name, text, hint }: { name: string; text: string; hint?: string }) {
  const { t } = useTranslation();
  return (
    <Stack gap={8}>
      <Cluster justify="between">
        <Text size="sm" tone="muted">
          {name}
        </Text>
        <CopyButton text={text} label={t("keys.guide.copy", { name })} />
      </Cluster>
      {/* Wrapped, not scrolled sideways: a scroll region needs its own tab stop, and a focusable `pre` is what the
          accessibility rules forbid. */}
      <CodeBlock as="pre" size="sm">
        {text}
      </CodeBlock>
      {hint ? (
        <Text size="sm" tone="faint">
          {hint}
        </Text>
      ) : null}
    </Stack>
  );
}

// The snippets leave the screen with the dialog that holds the plaintext key.
function Guide({ apiBase, secret }: { apiBase: string; secret: string }) {
  const { t } = useTranslation();
  const guide = keyGuide(apiBase, secret);
  if (!guide) {
    return null;
  }
  return (
    <Stack gap={12} rule>
      <Text as="h3" medium>
        {t("keys.guide.title")}
      </Text>
      <Stack gap={12}>
        <Text size="sm" tone="faint">
          {t("keys.guide.protocols")}
        </Text>
        {guide.protocols.map((entry) => (
          <Snippet key={entry.id} name={t(`keys.guide.protocol.${entry.id}`)} text={entry.text} />
        ))}
        <Text size="sm" tone="faint">
          {t("keys.guide.model")}
        </Text>
      </Stack>
      <Stack gap={12}>
        <Text size="sm" tone="faint">
          {t("keys.guide.clients")}
        </Text>
        {guide.clients.map((entry) => (
          <Snippet
            key={entry.id}
            name={t(`keys.guide.client.${entry.id}`)}
            text={entry.text}
            hint={t(`keys.guide.hint.${entry.id}`)}
          />
        ))}
      </Stack>
    </Stack>
  );
}

// A node before workspaces ignores the one asked for and makes the key in the default workspace; the answer's
// workspace is where the key is, for good (docs/domains/accounts.md「工作区」).
function Moved({ apiKey, asked }: { apiKey: Key; asked: string | undefined }) {
  const { t } = useTranslation();
  const { named } = useWorkspaces();
  if (asked === undefined || asked === apiKey.workspace_id) {
    return null;
  }
  const name = (id: string) => named(id) ?? id;
  return <FormError>{t("workspaces.key.moved", { actual: name(apiKey.workspace_id), asked: name(asked) })}</FormError>;
}

// The key's text exists only in this answer. The dialog does not close on Escape or a click outside, so it cannot be
// lost by accident; the user closes it once the key is copied.
export function SecretDialog({
  created,
  asked,
  onClose,
}: {
  created: Schemas["CreatedKey"];
  asked?: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const site = useQuery(siteQuery);
  return (
    <Dialog title={t("keys.secret.title")} onClose={onClose} dismissible={false}>
      <Stack>
        <Text tone="muted">{t("keys.secret.body", { name: created.key.name })}</Text>
        <Moved apiKey={created.key} asked={asked} />
        <CodeBlock>{created.secret}</CodeBlock>
        <Cluster justify="between">
          <CopyButton text={created.secret} />
          <Button variant="primary" size="md" onClick={onClose}>
            {t("keys.secret.done")}
          </Button>
        </Cluster>
        {site.isError ? (
          <Stack gap={8}>
            <FormError>{t("keys.guide.loadFailed")}</FormError>
            <Button disabled={site.isFetching} onClick={() => site.refetch()}>
              {t("state.retry", { ns: "common" })}
            </Button>
          </Stack>
        ) : (
          <Guide apiBase={site.data?.api_base_url ?? ""} secret={created.secret} />
        )}
      </Stack>
    </Dialog>
  );
}
