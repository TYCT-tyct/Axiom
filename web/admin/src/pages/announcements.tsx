import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { accountKey, toInstant, useErrors } from "@axiom/app";
import { ActionButton, Card, DataState, EmptyState, Field, Table, Td, Text, TextArea } from "@axiom/ui";
import { skipToken, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "../change";
import en from "../locales/en/announcements";
import zh from "../locales/zh/announcements";
import { Actions, Code, Head, State, Time } from "../parts";
import { SiteContentPage } from "../site-content-page";

export const messages = { zh: { announcements: zh }, en: { announcements: en } };

type Announcement = AdminSchemas["Announcement"];
type Published = Announcement & { withdrawn?: boolean };

export default function Announcements() {
  const { t } = useTranslation();
  return (
    <SiteContentPage title={t("nav.announcements")} description={t("announcements.description")}>
      {(siteId) => <Current siteId={siteId} />}
    </SiteContentPage>
  );
}

function Current({ siteId }: { siteId: string }) {
  const { t } = useTranslation();
  const cache = useQueryClient();
  const queryKey = [accountKey, "session-announcements", siteId];
  const session = useQuery<Published[]>({ queryKey, queryFn: skipToken, initialData: [], gcTime: Infinity });
  const [edit, setEdit] = useState<{ kind: "publish" } | { kind: "withdraw"; id?: string }>();
  const close = () => setEdit(undefined);
  const update = (change: (current: Published[]) => Published[]) =>
    cache.setQueryData<Published[]>(queryKey, (current) => (current ? change(current) : current));
  return (
    <>
      <Actions>
        <ActionButton variant="pri" icon="plus" onClick={() => setEdit({ kind: "publish" })}>
          {t("announcements.publish")}
        </ActionButton>
        <ActionButton onClick={() => setEdit({ kind: "withdraw" })}>{t("announcements.withdrawById")}</ActionButton>
      </Actions>
      <Card title={t("announcements.session")} layout="flush">
        <DataState
          query={session}
          isEmpty={(rows) => rows.length === 0}
          empty={<EmptyState title={t("announcements.empty")} />}
        >
          {(rows) => <AnnouncementsTable rows={rows} withdraw={(id) => setEdit({ kind: "withdraw", id })} />}
        </DataState>
      </Card>
      {edit?.kind === "publish" ? (
        <Publish
          siteId={siteId}
          onClose={close}
          onPublish={(row) => update((rows) => [row, ...rows.filter((item) => item.id !== row.id)])}
        />
      ) : null}
      {edit?.kind === "withdraw" ? (
        <Withdraw
          siteId={siteId}
          id={edit.id}
          onClose={close}
          onWithdraw={(id) => update((rows) => rows.map((row) => (row.id === id ? { ...row, withdrawn: true } : row)))}
        />
      ) : null}
    </>
  );
}

function AnnouncementsTable({ rows, withdraw }: { rows: Published[]; withdraw: (id: string) => void }) {
  const { t } = useTranslation();
  return (
    <Table label={t("announcements.session")}>
      <Head
        cols={[[t("announcements.title"), { main: true }], t("announcements.starts"), t("announcements.ends")]}
        action
      />
      <tbody>
        {rows.map((row) => (
          <tr key={row.id}>
            <Td main>
              <Text medium>{row.title}</Text>
              <Text size="sm" tone="faint" lines={2}>
                {row.body}
              </Text>
              <Code>{row.id}</Code>
            </Td>
            <Td>
              <Time at={row.starts_at} />
            </Td>
            <Td>
              <Time at={row.ends_at} />
            </Td>
            <Td actions>
              {row.withdrawn ? (
                <State tone="neutral">{t("announcements.withdrawn")}</State>
              ) : row.id ? (
                <ActionButton size="sm" onClick={() => withdraw(row.id as string)}>
                  {t("announcements.withdraw")}
                </ActionButton>
              ) : null}
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

function Publish({
  siteId,
  onClose,
  onPublish,
}: {
  siteId: string;
  onClose: () => void;
  onPublish: (row: Announcement) => void;
}) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`announcement-publish:${siteId}`}
      title={t("announcements.publish")}
      action={t("announcements.publish")}
      fields={(error) => <PublishFields error={error} />}
      read={(values): Omit<AdminSchemas["AnnouncementRequest"], "site_id" | "reason"> | Problem => {
        const title = values.title?.trim();
        const body = values.body?.trim();
        const starts_at = toInstant(values.starts_at);
        const ends_at = toInstant(values.ends_at);
        if (!title || !body || !starts_at || !ends_at || ends_at <= starts_at)
          return { problem: t("announcements.invalid") };
        return { title, body, starts_at, ends_at };
      }}
      send={async (content, { key, reason }) => {
        const result = await unwrapChange(
          adminApi.POST("/api/admin/v1/announcements", {
            params: { header: { "Idempotency-Key": key } },
            body: { site_id: siteId, ...content, reason },
          }),
        );
        if (result.data.id) onPublish(result.data);
        return result;
      }}
      done={(row) => (row.id ? <Code>{row.id}</Code> : null)}
      onClose={onClose}
    />
  );
}

function PublishFields({ error }: { error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <Field name="title" label={t("announcements.title")} maxLength={200} error={errors.field(error, "title")} />
      <TextArea
        name="body"
        label={t("announcements.body")}
        rows={6}
        maxLength={20_000}
        error={errors.field(error, "body")}
      />
      <Field
        name="starts_at"
        label={t("announcements.starts")}
        type="datetime-local"
        error={errors.field(error, "starts_at")}
      />
      <Field
        name="ends_at"
        label={t("announcements.ends")}
        type="datetime-local"
        error={errors.field(error, "ends_at")}
      />
    </>
  );
}

function Withdraw({
  siteId,
  id,
  onClose,
  onWithdraw,
}: {
  siteId: string;
  id?: string;
  onClose: () => void;
  onWithdraw: (id: string) => void;
}) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`announcement-withdraw:${siteId}:${id ?? "by-id"}`}
      title={t(id ? "announcements.withdraw" : "announcements.withdrawById")}
      action={t("announcements.withdraw")}
      danger
      intro={id ? <Code>{id}</Code> : undefined}
      fields={() => (id ? null : <Field name="id" label={t("announcements.id")} autoComplete="off" maxLength={30} />)}
      read={(values) => {
        const target = id ?? values.id?.trim();
        return target && /^ann_[0-9a-hjkmnp-tv-z]{26}$/.test(target)
          ? target
          : { problem: t("announcements.invalidId") };
      }}
      send={async (target, { key, reason }) => {
        const result = await unwrapChange(
          adminApi.POST("/api/admin/v1/announcements/{announcement_id}/withdraw", {
            params: { path: { announcement_id: target }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        );
        onWithdraw(target);
        return result;
      }}
      done={(result) => (result.id ? <Code>{result.id}</Code> : null)}
      onClose={onClose}
    />
  );
}
