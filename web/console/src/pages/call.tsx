import { can, identityOf, useSession } from "@axiom/app";
import { usePageTitle } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link, useLocation, useParams } from "@tanstack/react-router";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { callQuery, keysQuery } from "../queries";
import { StandaloneCallDetail } from "./calls-detail";
import { armCallsReturnScroll, getCallsReturnState } from "./calls-scroll";
import { CallsPageShell, usePhoneWidth } from "./calls-states";

export default function CallDetail() {
  const { t } = useTranslation();
  const session = useSession();
  const identity = identityOf(session);
  const phone = usePhoneWidth();
  const { callId } = useParams({ from: "/app/calls/$callId" });
  const callsFrom = useLocation({
    select: (l) => (l.state as { callsFrom?: string } | undefined)?.callsFrom,
  });
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

  const returnState = useMemo(() => getCallsReturnState(callsFrom, identity), [callsFrom, identity]);
  const restoreState =
    callsFrom && returnState.search
      ? (((prev: unknown) => ({
          ...(typeof prev === "object" && prev ? prev : {}),
          callsRestore: callsFrom,
        })) as never)
      : undefined;
  return (
    <CallsPageShell
      phone={phone}
      heading={t("callsBoard.detail.title")}
      headerActions={
        <Link
          to="/calls"
          search={returnState.search ?? {}}
          state={restoreState}
          resetScroll={!returnState.hasScroll}
          onClick={() => {
            if (callsFrom && returnState.search) armCallsReturnScroll(callsFrom, identity);
          }}
        >
          {t("callsBoard.detail.back")}
        </Link>
      }
    >
      <StandaloneCallDetail query={call} keyNames={keyNames} t={t} />
    </CallsPageShell>
  );
}
