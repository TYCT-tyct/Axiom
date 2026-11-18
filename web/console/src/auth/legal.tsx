import { ApiError, type Schemas } from "@axiom/api";
import ModelsCard from "@axiom/boards/AxApp/regions/ModelsCard";
import { SectionHeader } from "@axiom/boards/ds";
import { CheckField, FormError } from "@axiom/ui";
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

// An old node predates locale-aware documents and omits the field. Use zh for display and keys only; the acceptance
// omits it so the server resolves the row. Treating the absence as zh here keeps a page from hiding the terms entirely.
function documentLocale(document: Schemas["LegalDocument"]): Schemas["LegalDocumentLocale"] {
  return document.locale ?? "zh";
}

// The versions a form sends are exactly the ones the visitor saw. A document from an old node has no locale, so its
// acceptance also omits it and the server resolves the one current document from the kind and version; inventing zh
// here would name a different row after the locale expansion.
export function legalVersions(documents: Schemas["LegalDocument"][]): Schemas["LegalDocumentVersion"][] | undefined {
  return documents.length === 0
    ? undefined
    : documents.map((document) =>
        document.locale === undefined
          ? { kind: document.kind, version: document.version }
          : { kind: document.kind, locale: document.locale, version: document.version },
      );
}

// During the rolling-release compatibility phase an old node knows one current version per kind: the newest version
// across locales. Select that same version here, then use the console language only to choose its locale. Choosing a
// lower version in the preferred language would name a version the old node does not accept, and accepting at a new
// node would still leave an old node asking again. The cleanup migration, after old nodes exit, can switch this to
// independent per-locale streams.
export function legalDocumentsForLanguage(
  documents: Schemas["LegalDocument"][],
  language: string,
): Schemas["LegalDocument"][] {
  const preferred = language.toLowerCase().startsWith("zh") ? "zh" : "en";
  return (["terms", "privacy"] as const).flatMap((kind) => {
    const sameKind = documents.filter((document) => document.kind === kind);
    if (sameKind.length === 0) {
      return [];
    }
    const newest = Math.max(...sameKind.map((document) => document.version));
    const current = sameKind.filter((document) => document.version === newest);
    const document = current.find((candidate) => documentLocale(candidate) === preferred) ?? current[0];
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
  const { t: common } = useTranslation("common");
  if (consent.query.isPending) {
    return (
      <p role="status" className="text-label text-t4">
        {t("legal.loading")}
      </p>
    );
  }
  if (consent.query.isError) {
    if (consent.query.error instanceof ApiError && consent.query.error.status === 403) {
      return <FormError>{common("state.forbiddenTitle")}</FormError>;
    }
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
    <div className="board ax ov">
      {consent.documents.map((document) => {
        const title = t(`legal.${document.kind}`);
        return (
          <ModelsCard t={String} key={`${document.kind}:${documentLocale(document)}`}>
            <SectionHeader title={title} description={t("legal.version", { version: document.version })} />
            <LegalMarkdown body={document.body} label={title} />
          </ModelsCard>
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
