import { can, useSession } from "@axiom/app";
import { usePageTitle } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { callQuery, keysQuery } from "../queries";
import { StandaloneCallDetail } from "./calls-detail";
import { getLastCallsSearch, hasSavedCallsScroll } from "./calls-scroll";
import { CallsPageShell, usePhoneWidth } from "./calls-states";

export default function CallDetail() {
  const { t } = useTranslation();
  const session = useSession();
  const phone = usePhoneWidth();
  const { callId } = useParams({ from: "/app/calls/$callId" });
  const call = useQuery(callQuery(callId));
  const keys = useInfiniteQuery({ ...keysQuery({}), enabled: can(session, "api_keys.view") });
  usePageTitle(t("callsBoard.detail.title"));

  const keyNames = useMemo(() => {
    const map = new Map<string, string>();
    for (const p of keys.data?.pages ?? []) {
      for (const k of p.data) map.set(k.id, k.name || k.id);
    }
    return map;
  }, [keys.data]);

  const backSearch = useMemo(() => getLastCallsSearch() ?? {}, []);
  return (
    <CallsPageShell
      phone={phone}
      heading={t("callsBoard.detail.title")}
      headerActions={
        <Link to="/calls" search={backSearch} resetScroll={!hasSavedCallsScroll()}>
          {t("callsBoard.detail.back")}
        </Link>
      }
    >
      <StandaloneCallDetail query={call} keyNames={keyNames} t={t} />
    </CallsPageShell>
  );
}
