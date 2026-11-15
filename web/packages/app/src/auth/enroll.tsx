import { Button, FormError, Icon, useErrors } from "@axiom/ui";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Gate } from "../session";
import { TotpSetupDialog, useBeginTotp } from "../totp-dialogs";
import { AuthFrame, type Brand, OtherAccount } from "./frame";

// EnrollPage is where a surface that requires two-step verification sends a user who has none: the password was right,
// but nothing here opens until a factor is set up. Setting it up replaces the session with one that has passed it, and
// the gate then moves the user on.
export function EnrollPage({ brand }: { brand?: Brand }) {
  return (
    <Gate stage="enroll">
      <EnrollForm brand={brand} />
    </Gate>
  );
}

function EnrollForm({ brand }: { brand?: Brand }) {
  const { t } = useTranslation("common");
  const errors = useErrors();
  const begin = useBeginTotp();
  const [open, setOpen] = useState(false);
  return (
    <AuthFrame
      title={t("auth.enroll.title")}
      description={t("auth.enroll.description")}
      brand={brand}
      footer={<OtherAccount />}
    >
      {begin.isError ? <FormError>{errors.message(begin.error)}</FormError> : null}
      <Button
        variant="primary"
        size="md"
        disabled={begin.isPending}
        onClick={() => begin.mutate(undefined, { onSuccess: () => setOpen(true) })}
      >
        <Icon name="shield" />
        {t("auth.enroll.start")}
      </Button>
      {open && begin.data ? <TotpSetupDialog setup={begin.data} onClose={() => setOpen(false)} /> : null}
    </AuthFrame>
  );
}
