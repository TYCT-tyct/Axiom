import { DataState, PageHeader } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { versionQuery } from "../version";

export default function Overview() {
  const { t } = useTranslation();
  const version = useQuery(versionQuery);
  return (
    <>
      <PageHeader title={t("nav.overview")} description={t("overview.description")} />
      <section className="p-6">
        <div className="max-w-xl rounded-card border border-b1 bg-s1 p-4 shadow-panel">
          <h2 className="text-label tracking-wide text-t4 uppercase">{t("overview.version")}</h2>
          <DataState query={version}>
            {(data) => <p className="mt-2 font-mono break-all text-t1">{data.version}</p>}
          </DataState>
        </div>
      </section>
    </>
  );
}
