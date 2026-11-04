import type { Schemas } from "@axiom/api";
import { CheckField, FormError, Panel } from "@axiom/ui";
import { type UseQueryResult, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { legalQuery } from "../queries";
import { LegalMarkdown } from "./legal-markdown";

// useLegalConsent reads the site's current documents and the visitor's answer. The versions sent with registration are
// exactly the ones on screen; a later publication changes them, which clears the answer until the new text is seen.
export type LegalConsentState = {
  query: UseQueryResult<Schemas["LegalDocuments"]>;
  documents: Schemas["LegalDocument"][];
  accepted: boolean;
  setAccepted: (accepted: boolean) => void;
  versions: Schemas["LegalDocumentVersion"][] | undefined;
  ready: boolean;
  reload: () => void;
};

// An old node predates locale-aware documents and omits the field. Those rows are the Chinese text (the migration's
// default), so a new page treats the absence as zh instead of selecting no document and hiding the terms.
function documentLocale(document: Schemas["LegalDocument"]): Schemas["LegalDocumentLocale"] {
  return document.locale ?? "zh";
}

// The versions a form sends are exactly the ones the visitor saw, including their locale. No documents means no
// consent field at all. A missing locale is sent as zh, and an old node ignores the extra member.
export function legalVersions(documents: Schemas["LegalDocument"][]): Schemas["LegalDocumentVersion"][] | undefined {
  return documents.length === 0
    ? undefined
    : documents.map((document) => ({
        kind: document.kind,
        locale: documentLocale(document),
        version: document.version,
      }));
}

// The site may publish Chinese and English separately. Show one document per kind in the language the console uses,
// falling back to the other locale only when that kind has no document in the chosen one. Terms come first.
export function legalDocumentsForLanguage(
  documents: Schemas["LegalDocument"][],
  language: string,
): Schemas["LegalDocument"][] {
  const preferred = language.toLowerCase().startsWith("zh") ? "zh" : "en";
  const fallback = preferred === "zh" ? "en" : "zh";
  return (["terms", "privacy"] as const).flatMap((kind) => {
    const sameKind = documents.filter((document) => document.kind === kind);
    const document =
      sameKind.find((candidate) => documentLocale(candidate) === preferred) ??
      sameKind.find((candidate) => documentLocale(candidate) === fallback);
    return document ? [document] : [];
  });
}

// A form may submit once the documents loaded and, when there are any, the visitor accepted this exact version.
export function canSubmitWithLegal(consent: Pick<LegalConsentState, "ready" | "documents" | "accepted">): boolean {
  return consent.ready && (consent.documents.length === 0 || consent.accepted);
}

export function useLegalConsent(): LegalConsentState {
  const { i18n } = useTranslation();
  const query = useQuery(legalQuery);
  const [acceptedKey, setAcceptedKey] = useState<string | null>(null);
  const documents = legalDocumentsForLanguage(query.data?.documents ?? [], i18n.resolvedLanguage ?? i18n.language);
  const versionKey = documents
    .map((document) => `${document.kind}:${documentLocale(document)}:${document.version}`)
    .join("|");
  return {
    query,
    documents,
    accepted: acceptedKey === versionKey,
    setAccepted: (accepted) => setAcceptedKey(accepted ? versionKey : null),
    versions: legalVersions(documents),
    ready: query.isSuccess,
    reload: () => {
      setAcceptedKey(null);
      void query.refetch();
    },
  };
}

// LegalConsent shows the documents and the one checkbox that accepts them. A site with no documents shows nothing and
// needs no acceptance; a failed read blocks the form rather than letting it submit into consent_required.
export function LegalConsent({ consent }: { consent: LegalConsentState }) {
  const { t } = useTranslation();
  if (consent.query.isPending) {
    return <p className="text-label text-t4">{t("legal.loading")}</p>;
  }
  if (consent.query.isError) {
    return (
      <FormError>
        {t("legal.error")}{" "}
        <button type="button" className="underline underline-offset-4" onClick={consent.reload}>
          {t("legal.retry")}
        </button>
      </FormError>
    );
  }
  if (consent.documents.length === 0) {
    return null;
  }
  return (
    <div className="space-y-3">
      {consent.documents.map((document) => {
        const title = t(`legal.${document.kind}`);
        return (
          <Panel
            key={`${document.kind}:${documentLocale(document)}`}
            title={title}
            action={<span className="text-label text-t4">{t("legal.version", { version: document.version })}</span>}
          >
            <LegalMarkdown body={document.body} label={title} />
          </Panel>
        );
      })}
      <CheckField
        label={t("legal.accept")}
        checked={consent.accepted}
        onChange={(event) => consent.setAccepted(event.currentTarget.checked)}
      />
    </div>
  );
}
