import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { ActionButton, Card, DataState, EmptyState, Field, PageHead, Stack, Table, Td, TextArea } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, useSnapshot } from "../change";
import en from "../locales/en/alert-configuration";
import zh from "../locales/zh/alert-configuration";
import { formatJSON, parseJSONLossless } from "../lossless";
import { Actions, Code, Head, Json, PageBody } from "../parts";
import { alertConfigurationQuery } from "../policy-queries";

export const messages = { zh: { alertConfig: zh }, en: { alertConfig: en } };

type Config = AdminSchemas["AlertConfiguration"];
type ConfigInput = Omit<AdminSchemas["AlertConfigurationRequest"], "reason">;
type Channel = AdminSchemas["AlertChannel"];
type Edit = { kind: "configuration" } | { kind: "test" | "secret"; id: string };

export default function AlertConfiguration() {
  const { t } = useTranslation();
  const query = useQuery(alertConfigurationQuery);
  return (
    <>
      <PageHead title={t("nav.alertConfig")} lede={t("alertConfig.description")} />
      <PageBody>
        <DataState
          query={query}
          isEmpty={(view) => view.rev === undefined}
          empty={<EmptyState title={t("alertConfig.unavailable")} />}
        >
          {(view) => <Configuration view={view} />}
        </DataState>
      </PageBody>
    </>
  );
}

function Configuration({ view }: { view: Config }) {
  const { t } = useTranslation();
  const [edit, setEdit] = useState<Edit>();
  const close = () => setEdit(undefined);
  const rules = view.rules ?? [];
  const channels = view.channels ?? [];
  return (
    <>
      <Actions>
        <ActionButton variant="pri" icon="pencil" onClick={() => setEdit({ kind: "configuration" })}>
          {t("alertConfig.edit")}
        </ActionButton>
      </Actions>
      <Card title={t("alertConfig.rules")}>
        {rules.length ? (
          <Json label={t("alertConfig.rules")} value={rules} />
        ) : (
          <EmptyState title={t("alertConfig.noRules")} />
        )}
      </Card>
      <Card title={t("alertConfig.channels")} layout="flush">
        {channels.length ? (
          <Channels channels={channels} edit={setEdit} />
        ) : (
          <EmptyState title={t("alertConfig.noChannels")} />
        )}
      </Card>
      <Card title={t("alertConfig.spend")}>
        {view.spend_detection ? (
          <Json label={t("alertConfig.spend")} value={view.spend_detection} />
        ) : (
          <EmptyState title={t("alertConfig.noSpend")} />
        )}
      </Card>
      {edit?.kind === "configuration" ? <Replace live={view} onClose={close} /> : null}
      {edit?.kind === "test" ? <TestChannel id={edit.id} onClose={close} /> : null}
      {edit?.kind === "secret" ? <Rotate id={edit.id} live={view} onClose={close} /> : null}
    </>
  );
}

function Channels({ channels, edit }: { channels: Channel[]; edit: (value: Edit) => void }) {
  const { t } = useTranslation();
  return (
    <Table label={t("alertConfig.channels")}>
      <Head cols={[[t("alertConfig.channel"), { main: true }], t("alertConfig.destination")]} action />
      <tbody>
        {channels.map((channel) => {
          const { id } = channel;
          if (!id) return null;
          return (
            <tr key={id}>
              <Td main>
                <Code>{id}</Code>
              </Td>
              <Td>{channel.email ?? channel.webhook_url ?? "—"}</Td>
              <Td actions>
                <ActionButton size="sm" onClick={() => edit({ kind: "test", id })}>
                  {t("alertConfig.testChannel")}
                </ActionButton>
                {channel.webhook_url ? (
                  <ActionButton size="sm" onClick={() => edit({ kind: "secret", id })}>
                    {t("alertConfig.rotate")}
                  </ActionButton>
                ) : null}
              </Td>
            </tr>
          );
        })}
      </tbody>
    </Table>
  );
}

function readConfiguration(rev: number, values: Record<string, string>): ConfigInput | undefined {
  try {
    const rules = parseJSONLossless(values.rules ?? "");
    const channels = parseJSONLossless(values.channels ?? "");
    const spend = values.spend_detection?.trim() ? parseJSONLossless(values.spend_detection) : undefined;
    if (
      !Array.isArray(rules) ||
      !Array.isArray(channels) ||
      (spend !== undefined && (!spend || typeof spend !== "object" || Array.isArray(spend)))
    ) {
      return undefined;
    }
    // Keep parsed numbers exact. The API validates the fields and their bounds.
    return { rev, rules, channels, ...(spend === undefined ? {} : { spend_detection: spend }) } as ConfigInput;
  } catch {
    return undefined;
  }
}

function Replace({ live, onClose }: { live: Config; onClose: () => void }) {
  const { t } = useTranslation();
  const [view, reload] = useSnapshot(live);
  const rev = view.rev;
  if (rev === undefined) return null;
  return (
    <ChangeDialog
      operation="alert-configuration"
      title={t("alertConfig.edit")}
      action={t("alertConfig.save")}
      intro={<p>{t("alertConfig.editIntro")}</p>}
      fields={(error) => <ConfigurationFields view={view} error={error} />}
      read={(values) => readConfiguration(rev, values) ?? { problem: t("alertConfig.invalid") }}
      send={(values, { key, reason }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/alerts/configuration", {
            params: { header: { "Idempotency-Key": key } },
            body: { ...values, reason },
            bodySerializer: formatJSON,
          }),
        )
      }
      done={() => <p>{t("alertConfig.saved")}</p>}
      reload={reload}
      onClose={onClose}
    />
  );
}

function ConfigurationFields({ view, error }: { view: Config; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <Stack>
      <TextArea
        label={t("alertConfig.rules")}
        name="rules"
        rows={7}
        defaultValue={formatJSON(view.rules ?? [])}
        error={errors.field(error, "rules")}
        spellCheck={false}
      />
      <TextArea
        label={t("alertConfig.channels")}
        name="channels"
        rows={5}
        defaultValue={formatJSON(view.channels ?? [])}
        error={errors.field(error, "channels")}
        spellCheck={false}
      />
      <TextArea
        label={t("alertConfig.spend")}
        name="spend_detection"
        rows={6}
        defaultValue={view.spend_detection ? formatJSON(view.spend_detection) : ""}
        error={errors.field(error, "spend_detection")}
        spellCheck={false}
      />
    </Stack>
  );
}

function TestChannel({ id, onClose }: { id: string; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`test-alert-channel:${id}`}
      title={t("alertConfig.testChannel")}
      action={t("alertConfig.testChannel")}
      intro={<Code>{id}</Code>}
      read={() => id}
      send={(channel, { key, reason }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/alerts/tests", {
            params: { header: { "Idempotency-Key": key } },
            body: { channel, reason },
          }),
        )
      }
      done={(result) => (
        <p>
          {t("alertConfig.queued")} {result.id ? <Code>{result.id}</Code> : null}
        </p>
      )}
      onClose={onClose}
    />
  );
}

function Rotate({ id, live, onClose }: { id: string; live: Config; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [view, reload] = useSnapshot(live);
  const rev = view.rev;
  if (rev === undefined) return null;
  return (
    <ChangeDialog
      operation={`rotate-alert-channel-secret:${id}`}
      title={t("alertConfig.rotate")}
      action={t("alertConfig.rotate")}
      intro={
        <p>
          {t("alertConfig.rotateIntro")} <Code>{id}</Code>
        </p>
      }
      fields={(error) => (
        <Field
          label={t("alertConfig.secret")}
          name="secret"
          type="password"
          autoComplete="new-password"
          error={errors.field(error, "secret")}
        />
      )}
      read={({ secret }) => {
        const size = new TextEncoder().encode(secret).length;
        return secret && size >= 32 && size <= 512 ? secret : { problem: t("alertConfig.invalidSecret") };
      }}
      send={(secret, { key, reason }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/alerts/channels/{channel_id}/secret", {
            params: { path: { channel_id: id }, header: { "Idempotency-Key": key } },
            body: { rev, secret, reason },
          }),
        )
      }
      done={() => <p>{t("alertConfig.rotated")}</p>}
      reload={reload}
      onClose={onClose}
    />
  );
}
