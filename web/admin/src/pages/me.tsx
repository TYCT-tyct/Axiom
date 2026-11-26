import { RecoveryDialog, SignOutError, useSession, useSignOut } from "@axiom/app";
import { ActionButton, Card, Id, PageHead, SettingRow, Tag, useFormat } from "@axiom/ui";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import en from "../locales/en/me";
import zh from "../locales/zh/me";
import { PageBody } from "../parts";

export const messages = { zh: { me: zh }, en: { me: en } };

// The person's own sign-in (adPg_me in admin_pages_c.js), reached from the avatar's menu: this session, signing out of
// it, and the two-step verification the admin always asks for. The session is the one the shell already holds. The
// recovery codes a regeneration makes are shown once, above everything, by the host every page sits under, which keeps
// them until they are saved; two-step verification has no off switch here or anywhere.
export default function Me() {
  const { t } = useTranslation();
  const format = useFormat();
  const session = useSession();
  const signOut = useSignOut();
  const [regenerating, setRegenerating] = useState(false);
  const email = session.user.email;
  return (
    <>
      <PageBody>
        <PageHead title={t("shell.myLogin")} lede={t("me.description")} />
        <Card title={t("me.account.title")} layout="rows">
          <SettingRow title={email.split("@")[0] ?? email} detail={email}>
            <Id value={session.account} />
          </SettingRow>
          <SettingRow
            title={t("me.session.title")}
            detail={t("me.session.detail", { at: format.dateTime(new Date(session.expires_at)) })}
          >
            <ActionButton disabled={signOut.isPending} onClick={() => signOut.mutate()}>
              {t("me.session.signOut")}
            </ActionButton>
          </SettingRow>
          <SignOutError error={signOut.error} />
        </Card>
        <Card title={t("me.twoStep.title")} lede={t("me.twoStep.lede")} layout="rows">
          <SettingRow title={t("me.twoStep.verifier")} detail={t("me.twoStep.verifierDetail")}>
            <Tag tone={session.mfa_enabled ? "ok" : "warn"} dot>
              {t(session.mfa_enabled ? "me.twoStep.bound" : "me.twoStep.unbound")}
            </Tag>
          </SettingRow>
          <SettingRow title={t("me.twoStep.codes")} detail={t("me.twoStep.codesDetail")}>
            <ActionButton onClick={() => setRegenerating(true)}>{t("me.twoStep.regenerate")}</ActionButton>
          </SettingRow>
        </Card>
      </PageBody>
      {regenerating ? <RecoveryDialog onClose={() => setRegenerating(false)} /> : null}
    </>
  );
}
