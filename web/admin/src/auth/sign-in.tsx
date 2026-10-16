import { SignInPage } from "@axiom/app";
import { useTranslation } from "react-i18next";

export default function SignIn() {
  const { t } = useTranslation();
  return <SignInPage brand={{ name: t("brandName") }} description={t("signIn.description")} />;
}
