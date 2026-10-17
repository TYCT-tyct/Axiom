import { can, useSession } from "@axiom/app";
import { PageHeader } from "@axiom/ui";
import { Navigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { nav } from "../nav";

// The admin has no overview page in M1: the entry address opens the first page the session may use.
export default function Home() {
  const { t } = useTranslation();
  const session = useSession();
  const first = nav.find((item) => can(session, item.permission));
  if (first) {
    return <Navigate to={first.to} replace />;
  }
  return <PageHeader title={t("home.noAccessTitle")} description={t("home.noAccessBody")} />;
}
