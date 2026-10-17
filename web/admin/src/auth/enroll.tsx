import { EnrollPage } from "@axiom/app";
import { useTranslation } from "react-i18next";

export default function Enroll() {
  const { t } = useTranslation();
  return <EnrollPage brand={{ name: t("brandName") }} />;
}
