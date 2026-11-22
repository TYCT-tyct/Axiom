import { ApiError, type Schemas } from "@axiom/api";
import LoginConsent from "@axiom/boards/Login/regions/LoginConsent";
import LoginNote from "@axiom/boards/Login/regions/LoginNote";
import { DocumentText } from "@axiom/ui";
import { type UseQueryResult, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { legalQuery } from "../queries";
import { legalBlocks } from "./legal-markdown";
import { FormProblem, NoteAction } from "./login-parts";
import { useHoldReveal } from "./reveal-hold";

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
  // the page shows the documents and their box: an arrival waits for them rather than show the button grey
  useHoldReveal(query.isPending);
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

// What a form that accepts the documents shows of them: the read under way, refused (403, no retry) or failed (with a
// retry), none to accept, or the documents and the box. The forms that accept them (registration, the verification
// page, the consent page) show these.
function legalView(consent: LegalConsentState) {
  const { query } = consent;
  if (query.isPending) return "loading";
  if (query.isError) return query.error instanceof ApiError && query.error.status === 403 ? "forbidden" : "failed";
  return consent.documents.length === 0 ? "none" : "documents";
}

// LegalConsent shows the documents and the one box that accepts them, or why they are not there: the read under way,
// refused (403, no retry) or failed (with a retry). A site with no documents shows nothing and needs no acceptance; a
// failed read blocks the form rather than letting it submit into consent_required. The box shows the answer the form
// would send: a new version on screen, or a reload after a refusal, clears it.
export function LegalConsent({ consent }: { consent: LegalConsentState }) {
  const { t } = useTranslation();
  const { t: common } = useTranslation("common");
  const view = legalView(consent);
  if (view === "loading") {
    return (
      <LoginNote t={String}>
        <span role="status">{t("legal.loading")}</span>
      </LoginNote>
    );
  }
  if (view === "forbidden") return <FormProblem message={common("state.forbiddenTitle")} />;
  if (view === "failed") {
    return (
      <>
        <FormProblem message={t("legal.error")} />
        <NoteAction label={t("legal.retry")} onPress={consent.reload} />
      </>
    );
  }
  if (view === "none") return null;
  return (
    <>
      <LegalDocuments documents={consent.documents} />
      <LoginConsent
        t={String}
        label={t("legal.accept")}
        values={{
          checked: consent.accepted,
          onToggle: (event) => consent.setAccepted((event.currentTarget as HTMLInputElement).checked),
        }}
      />
    </>
  );
}

// LegalDocuments shows each document with its version, as the form that accepts them sends it.
function LegalDocuments({ documents }: { documents: Schemas["LegalDocument"][] }) {
  const { t } = useTranslation();
  return documents.map((document) => (
    <DocumentText
      key={`${document.kind}:${documentLocale(document)}`}
      title={t(`legal.${document.kind}`)}
      lede={t("legal.version", { version: document.version })}
      blocks={legalBlocks(document.body)}
    />
  ));
}
