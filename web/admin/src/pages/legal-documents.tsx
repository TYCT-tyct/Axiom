import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Dialog } from "@axiom/boards/ds";
import { ActionButton, Card, DataState, EmptyState, LoadMore, Select, Table, Td, Text, TextArea } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "../change";
import en from "../locales/en/legal-documents";
import zh from "../locales/zh/legal-documents";
import { Actions, Head, Time } from "../parts";
import { legalDocumentQuery } from "../policy-queries";
import { SiteContentPage } from "../site-content-page";

export const messages = { zh: { legalDocs: zh }, en: { legalDocs: en } };

type Document = AdminSchemas["LegalDocument"];

export default function LegalDocuments() {
  const { t } = useTranslation();
  return (
    <SiteContentPage title={t("nav.legalDocs")} description={t("legalDocs.description")}>
      {(siteId) => <History siteId={siteId} />}
    </SiteContentPage>
  );
}

function History({ siteId }: { siteId: string }) {
  const { t } = useTranslation();
  const query = useInfiniteQuery(legalDocumentQuery(siteId));
  const [selected, setSelected] = useState<Document>();
  const [publishing, setPublishing] = useState(false);
  const rows = query.data?.pages.flatMap((page) => page.documents) ?? [];
  return (
    <>
      {query.isSuccess ? (
        <Actions>
          <ActionButton variant="pri" icon="plus" onClick={() => setPublishing(true)}>
            {t("legalDocs.publish")}
          </ActionButton>
        </Actions>
      ) : null}
      <Card title={t("legalDocs.history")} layout="flush">
        <DataState query={query} isEmpty={() => rows.length === 0} empty={<EmptyState title={t("legalDocs.empty")} />}>
          {() => <Versions documents={rows} select={setSelected} />}
        </DataState>
        <LoadMore list={query} />
      </Card>
      {selected ? (
        <Dialog
          title={t("legalDocs.document", { kind: t(`legalDocs.kinds.${selected.kind}`), version: selected.version })}
          closeLabel={t("dialog.close", { ns: "common" })}
          onClose={() => setSelected(undefined)}
          primary={{ label: t("change.close"), onClick: () => setSelected(undefined) }}
        >
          <Text breaks prose>
            {selected.body}
          </Text>
        </Dialog>
      ) : null}
      {publishing ? <Publish siteId={siteId} onClose={() => setPublishing(false)} /> : null}
    </>
  );
}

function Versions({ documents, select }: { documents: Document[]; select: (document: Document) => void }) {
  const { t } = useTranslation();
  return (
    <Table label={t("legalDocs.history")}>
      <Head
        cols={[
          [t("legalDocs.kind"), { main: true }],
          t("legalDocs.locale"),
          t("legalDocs.version"),
          t("legalDocs.publishedAt"),
        ]}
        action
      />
      <tbody>
        {documents.map((document) => (
          <tr key={`${document.kind}/${document.locale ?? "zh"}/${document.version}`}>
            <Td main>{t(`legalDocs.kinds.${document.kind}`)}</Td>
            <Td>{t(`legalDocs.languages.${document.locale ?? "zh"}`)}</Td>
            <Td>{document.version}</Td>
            <Td>
              <Time at={document.published_at} />
            </Td>
            <Td actions>
              <ActionButton size="sm" onClick={() => select(document)}>
                {t("legalDocs.read")}
              </ActionButton>
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

function Publish({ siteId, onClose }: { siteId: string; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`legal-document-publish:${siteId}`}
      title={t("legalDocs.publish")}
      action={t("legalDocs.publish")}
      intro={<p>{t("legalDocs.publishIntro")}</p>}
      fields={(error) => <PublishFields error={error} />}
      read={({ kind, locale, body }): Omit<AdminSchemas["LegalDocumentPublish"], "reason"> | Problem => {
        if ((kind !== "terms" && kind !== "privacy") || (locale !== "zh" && locale !== "en") || !body?.trim()) {
          return { problem: t("legalDocs.invalid") };
        }
        return { kind, locale, body } satisfies Omit<AdminSchemas["LegalDocumentPublish"], "reason">;
      }}
      send={(values, { key, reason }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/sites/{site_id}/legal-documents", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { ...values, reason },
          }),
        )
      }
      done={(document) => (
        <p>{t("legalDocs.document", { kind: t(`legalDocs.kinds.${document.kind}`), version: document.version })}</p>
      )}
      onClose={onClose}
    />
  );
}

function PublishFields({ error }: { error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <Select
        label={t("legalDocs.kind")}
        name="kind"
        options={(["terms", "privacy"] as const).map((value) => ({ value, label: t(`legalDocs.kinds.${value}`) }))}
      />
      <Select
        label={t("legalDocs.locale")}
        name="locale"
        options={(["zh", "en"] as const).map((value) => ({ value, label: t(`legalDocs.languages.${value}`) }))}
      />
      <TextArea
        label={t("legalDocs.body")}
        name="body"
        rows={9}
        maxLength={65_536}
        error={errors.field(error, "body")}
      />
    </>
  );
}
