import { SignInPage } from "@axiom/app";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { siteQuery } from "../queries";

export default function SignIn() {
  const { t } = useTranslation();
  const next = useSearch({ strict: false }).next;
  const site = useQuery(siteQuery);
  return (
    <SignInPage
      brand={site.data}
      description={t("auth.signIn.description")}
      footer={
        <>
          {t("auth.signIn.noAccount")}{" "}
          <Link
            to="/register"
            search={{ next, invite: undefined }}
            className="text-accent underline underline-offset-4"
          >
            {t("auth.signIn.register")}
          </Link>
        </>
      }
    />
  );
}
