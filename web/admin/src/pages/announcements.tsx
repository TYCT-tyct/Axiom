import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { LoadMore, toInstant, useErrors } from "@axiom/app";
import {
  ActionButton,
  Announcement,
  Card,
  DataState,
  EmptyState,
  Field,
  Grid,
  Id,
  List,
  SettingRow,
  Tag,
  Text,
  TextArea,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "../change";
import { announcementsQuery } from "../content-queries";
import en from "../locales/en/announcements";
import zh from "../locales/zh/announcements";
import { Actions, Code } from "../parts";
import { notBuilt } from "../plugins";
import { SiteContentPage } from "../site-content-page";

export const messages = { zh: { announcements: zh }, en: { announcements: en } };

type Item = AdminSchemas["AdminAnnouncement"];

const tones = { scheduled: "accent", showing: "ok", ended: "neutral", withdrawn: "neutral" } as const;

export default function Announcements() {
  const { t } = useTranslation();
  return (
    <SiteContentPage title={t("shell.nav.announcements")} description={t("announcements.description")}>
      {(siteId) => <Current siteId={siteId} />}
    </SiteContentPage>
  );
}

// The announcements of the site, and beside them what its users see now (adPg_ann in admin_pages_d.js). A node that has
// no list yet (501) shows the page as it is before any announcement: publishing still works.
function Current({ siteId }: { siteId: string }) {
  const { t } = useTranslation();
  const list = useInfiniteQuery(announcementsQuery(siteId));
  const rows = list.data?.pages.flatMap((page) => page.data) ?? [];
  const [edit, setEdit] = useState<"publish" | Item>();
  const publish = (
    <ActionButton variant="pri" icon="plus" onClick={() => setEdit("publish")}>
      {t("announcements.publish")}
    </ActionButton>
  );
  const none = (
    <EmptyState title={t("announcements.empty.title")} action={publish}>
      {t("announcements.empty.body")}
    </EmptyState>
  );
  return (
    <>
      {rows.length > 0 ? <Actions>{publish}</Actions> : null}
      {notBuilt(list.error) ? (
        none
      ) : (
        <DataState query={list} isEmpty={() => rows.length === 0} empty={none}>
          {() => (
            <Grid columns={2}>
              <Card title={t("announcements.listTitle")} lede={t("announcements.listLede")} layout="rows">
                <List plain>
                  {rows.map((row) => (
                    <Row key={row.id} row={row} withdraw={() => setEdit(row)} />
                  ))}
                </List>
                <LoadMore list={list} />
              </Card>
              <Preview rows={rows} />
            </Grid>
          )}
        </DataState>
      )}
      {edit === "publish" ? <Publish siteId={siteId} onClose={() => setEdit(undefined)} /> : null}
      {edit && edit !== "publish" ? <Withdraw row={edit} onClose={() => setEdit(undefined)} /> : null}
    </>
  );
}

function Row({ row, withdraw }: { row: Item; withdraw: () => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <SettingRow
      as="li"
      title={row.title}
      detail={
        <>
          <Text as="span" size="sm" tone="muted" lines={2}>
            {row.body}
          </Text>
          <Text as="div" size="sm" tone="faint">
            <Id value={row.id} />{" "}
            {t("announcements.range", {
              from: format.dateTime(new Date(row.starts_at)),
              to: format.dateTime(new Date(row.ends_at)),
            })}
          </Text>
        </>
      }
    >
      <Tag tone={tones[row.state]}>{t(`announcements.state.${row.state}`)}</Tag>
      {row.state === "scheduled" || row.state === "showing" ? (
        <ActionButton size="sm" onClick={withdraw}>
          {t("announcements.withdraw")}
        </ActionButton>
      ) : null}
    </SettingRow>
  );
}

// What users see now, drawn as the console draws it. The list is newest start first, so the first one showing is the
// newest; the others follow it in turn. One that has not started is not shown, only counted.
function Preview({ rows }: { rows: Item[] }) {
  const { t } = useTranslation();
  const format = useFormat();
  const showing = rows.filter((row) => row.state === "showing");
  const waiting = rows.filter((row) => row.state === "scheduled");
  const shown = showing[0];
  const next = waiting.reduce<Item | undefined>(
    (soonest, row) => (soonest && soonest.starts_at <= row.starts_at ? soonest : row),
    undefined,
  );
  return (
    <Card title={t("announcements.preview.title")} lede={t("announcements.preview.lede")}>
      {shown ? (
        <Announcement title={shown.title}>
          <Text tone="muted">{shown.body}</Text>
        </Announcement>
      ) : (
        <Text tone="muted">{t("announcements.preview.none")}</Text>
      )}
      {shown ? (
        <Text size="sm" tone="faint">
          {t("announcements.preview.until", { at: format.dateTime(new Date(shown.ends_at)) })}
          {showing.length > 1 ? ` ${t("announcements.preview.more", { count: showing.length - 1 })}` : ""}
        </Text>
      ) : waiting.length === 0 ? (
        <Text size="sm" tone="faint">
          {t("announcements.preview.noneHint")}
        </Text>
      ) : null}
      {next ? (
        <Text size="sm" tone="faint">
          {t("announcements.preview.waiting", {
            count: waiting.length,
            at: format.dateTime(new Date(next.starts_at)),
          })}
        </Text>
      ) : null}
    </Card>
  );
}

function Publish({ siteId, onClose }: { siteId: string; onClose: () => void }) {
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
      send={(content, { key, reason }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/announcements", {
            params: { header: { "Idempotency-Key": key } },
            body: { site_id: siteId, ...content, reason },
          }),
        )
      }
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

// A withdrawal has no way back: no undo is offered, and the dialog says so.
function Withdraw({ row, onClose }: { row: Item; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`announcement-withdraw:${row.site_id}:${row.id}`}
      title={t("announcements.withdrawTitle")}
      action={t("announcements.withdraw")}
      danger
      intro={<Code>{row.id}</Code>}
      diff={[
        {
          label: row.title,
          before: t(`announcements.state.${row.state}`),
          after: t("announcements.state.withdrawn"),
        },
      ]}
      warn={t("announcements.withdrawWarn")}
      read={() => row.id}
      send={(id, { key, reason }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/announcements/{announcement_id}/withdraw", {
            params: { path: { announcement_id: id }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      toast={() => t("announcements.withdrawnToast")}
      done={(result) => (result.id ? <Code>{result.id}</Code> : null)}
      onClose={onClose}
    />
  );
}
