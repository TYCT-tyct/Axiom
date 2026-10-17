import { consoleApi, unwrap } from "@axiom/api";
import { Badge, Button, CopyButton, FormError, Icon, PageHeader, Panel } from "@axiom/ui";
import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useErrors } from "../errors";
import { useSession } from "../session";
import { RecoveryDialog, TotpSetupDialog } from "../totp-dialogs";

export default function Security() {
  const { t } = useTranslation();
  return (
    <>
      <PageHeader title={t("nav.security")} description={t("security.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        <TwoStep />
        <Invite />
      </div>
    </>
  );
}

// Starting to enable asks for a new secret and opens the dialog that proves it; nothing is stored until it is proved.
function TwoStep() {
  const { t } = useTranslation();
  const errors = useErrors();
  const session = useSession();
  const [dialog, setDialog] = useState<"recovery" | null>(null);
  const begin = useMutation({
    mutationFn: () => unwrap(consoleApi.POST("/api/console/v1/auth/totp/setup")),
  });
  const setup = begin.data;
  return (
    <Panel
      title={t("security.twoStep.title")}
      action={
        <Badge tone={session.mfa_enabled ? "ok" : "warn"} icon={session.mfa_enabled ? "check" : "alert"}>
          {session.mfa_enabled ? t("security.twoStep.on") : t("security.twoStep.off")}
        </Badge>
      }
    >
      <div className="space-y-4 p-4">
        <p className="max-w-prose text-t2">{t("security.twoStep.body")}</p>
        {begin.isError ? <FormError>{errors.message(begin.error)}</FormError> : null}
        <div className="flex flex-wrap gap-2">
          <Button
            variant={session.mfa_enabled ? "secondary" : "primary"}
            size="md"
            onClick={() => begin.mutate()}
            disabled={begin.isPending}
          >
            <Icon name={session.mfa_enabled ? "refresh" : "shield"} />
            {session.mfa_enabled ? t("security.twoStep.replace") : t("security.twoStep.enable")}
          </Button>
          {session.mfa_enabled ? (
            <Button size="md" onClick={() => setDialog("recovery")}>
              {t("security.twoStep.regenerate")}
            </Button>
          ) : null}
        </div>
      </div>
      {setup ? <TotpSetupDialog setup={setup} onClose={() => begin.reset()} /> : null}
      {dialog === "recovery" ? <RecoveryDialog onClose={() => setDialog(null)} /> : null}
    </Panel>
  );
}

function Invite() {
  const { t } = useTranslation();
  const { user } = useSession();
  const link = `${window.location.origin}${import.meta.env.BASE_URL}register?invite=${encodeURIComponent(user.invite_code)}`;
  return (
    <Panel title={t("security.account.title")}>
      <dl className="divide-y divide-b1">
        <div className="grid gap-x-4 gap-y-1 px-4 py-3 sm:grid-cols-[12rem_minmax(0,1fr)]">
          <dt className="text-t3">{t("auth.email")}</dt>
          <dd className="text-t1 [overflow-wrap:anywhere]">{user.email}</dd>
        </div>
        <div className="grid gap-x-4 gap-y-1 px-4 py-3 sm:grid-cols-[12rem_minmax(0,1fr)]">
          <dt className="text-t3">{t("security.account.invite")}</dt>
          <dd className="min-w-0 space-y-2">
            <p className="font-mono text-t1 [overflow-wrap:anywhere]">{user.invite_code}</p>
            <p className="text-label text-t3 [overflow-wrap:anywhere]">{link}</p>
            <div className="flex items-center gap-3">
              <CopyButton text={link} />
            </div>
          </dd>
        </div>
      </dl>
    </Panel>
  );
}
