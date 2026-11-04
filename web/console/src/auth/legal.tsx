import type { Schemas } from "@axiom/api";
import { CheckField, FormError } from "@axiom/ui";
import { type UseQueryResult, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { legalQuery } from "../queries";

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

// The versions a form sends are exactly the ones the visitor saw. No documents means no consent field at all.
export function legalVersions(documents: Schemas["LegalDocument"][]): Schemas["LegalDocumentVersion"][] | undefined {
  return documents.length === 0
    ? undefined
    : documents.map((document) => ({ kind: document.kind, version: document.version }));
}

// A form may submit once the documents loaded and, when there are any, the visitor accepted this exact version.
export function canSubmitWithLegal(consent: Pick<LegalConsentState, "ready" | "documents" | "accepted">): boolean {
  return consent.ready && (consent.documents.length === 0 || consent.accepted);
}

export function useLegalConsent(): LegalConsentState {
  const query = useQuery(legalQuery);
  const [acceptedKey, setAcceptedKey] = useState<string | null>(null);
  const documents = query.data?.documents ?? [];
  const versionKey = documents.map((document) => `${document.kind}:${document.version}`).join("|");
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

// LegalText is one scrollable document. It is focusable because a keyboard user cannot otherwise reach the overflow.
function LegalText({ document }: { document: Schemas["LegalDocument"] }) {
  const labelId = `legal-${document.kind}-${document.version}`;
  return (
    // biome-ignore format: the lint suppression applies to the line holding tabIndex.
    // biome-ignore lint/a11y/noNoninteractiveTabindex: Keyboard users need focus to scroll the document.
    <section tabIndex={0} aria-labelledby={labelId} className="max-h-56 overflow-y-auto whitespace-pre-wrap text-label text-t3 [overflow-wrap:anywhere] focus-visible:outline-2 focus-visible:outline-accent focus-visible:outline-offset-2">
      {document.body}
    </section>
  );
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
      {consent.documents.map((document) => (
        <section key={document.kind} className="space-y-1.5 rounded-panel border border-line-1 p-3">
          <h2
            id={`legal-${document.kind}-${document.version}`}
            className="flex items-baseline justify-between gap-2 text-ui text-t1"
          >
            {t(`legal.${document.kind}`)}
            <span className="text-label text-t4">{t("legal.version", { version: document.version })}</span>
          </h2>
          <LegalText document={document} />
        </section>
      ))}
      <CheckField
        label={t("legal.accept")}
        checked={consent.accepted}
        onChange={(event) => consent.setAccepted(event.currentTarget.checked)}
      />
    </div>
  );
}
