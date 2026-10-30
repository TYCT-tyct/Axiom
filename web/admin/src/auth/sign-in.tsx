import { SignInPage } from "@axiom/app";
import { useTranslation } from "react-i18next";
import { useAdminBrand } from "../brand";

export default function SignIn() {
  const { t } = useTranslation();
  const brand = useAdminBrand();
  return <SignInPage brand={brand} description={t("signIn.description")} />;
}
