import { MfaPage } from "@axiom/app";
import { useTranslation } from "react-i18next";

export default function Mfa() {
  const { t } = useTranslation();
  return <MfaPage brand={{ name: t("brandName") }} />;
}
