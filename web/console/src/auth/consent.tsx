import { ApiError, consoleApi, type Schemas, unwrapEmpty } from "@axiom/api";
import { AuthFrame, Gate, OtherAccount, useErrors, useSession } from "@axiom/app";
import { FormError, SubmitButton } from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { siteQuery } from "../queries";
import { canSubmitWithLegal, LegalConsent, useLegalConsent } from "./legal";

// A member whose accepted versions are behind the site's current ones lands here, outside the shell: every shell page
// would be refused with consent_required until this succeeds.
export default function Consent() {
  return (
    <Gate stage="consent">
      <ConsentPage />
    </Gate>
  );
}

function ConsentPage() {
  const { t } = useTranslation();
  const errors = useErrors();
  const session = useSession();
  const site = useQuery(siteQuery);
  const legal = useLegalConsent();
  const client = useQueryClient();
  const accept = useMutation({
    mutationFn: (body: Schemas["LegalAcceptance"]) =>
      unwrapEmpty(consoleApi.POST("/api/console/v1/legal/accept", { body })),
    onSuccess: () => client.invalidateQueries({ queryKey: ["session"] }),
    onError: (error) => {
      if (error instanceof ApiError && error.code === "consent_required") {
        legal.reload();
      }
    },
  });
  const missing = legal.query.isSuccess && legal.documents.length === 0;
  return (
    <AuthFrame
      brand={site.data}
      wide
      title={t("legal.title")}
      description={t("legal.description", { email: session.user.email })}
      footer={<OtherAccount />}
    >
      <form
        noValidate
        className="space-y-4"
        onSubmit={(event) => {
          event.preventDefault();
          if (!accept.isPending && canSubmitWithLegal(legal) && !missing) {
            accept.mutate({ consent: legal.versions ?? [] });
          }
        }}
      >
        {accept.error ? <FormError>{errors.message(accept.error)}</FormError> : null}
        {missing ? <FormError>{t("legal.missing")}</FormError> : null}
        <LegalConsent consent={legal} />
        <SubmitButton pending={accept.isPending} disabled={!canSubmitWithLegal(legal) || missing} className="w-full">
          {t("legal.acceptSubmit")}
        </SubmitButton>
      </form>
    </AuthFrame>
  );
}
