import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { useFormat } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { discrepanciesQuery } from "./finance-queries";
import type { Pending } from "./nav";
import { alertEventsQuery } from "./policy-queries";
import { accountQuery, approvalsQuery, paymentRefundsQuery, pendingMappingsQuery } from "./queries";

// What waits on the pages the sidebar marks with a count (adBadge in admin_core.js), read from the lists those pages
// show, first page only: a count is the first page's, which no queue here outgrows. A count the session may not read,
// or whose list has not answered, is not shown.

type Session = ReturnType<typeof useSession>;
type Page<T> = { pages: { data?: T[] | null }[] } | undefined;
const firstPage = <T>(data: Page<T>) => data?.pages[0]?.data ?? undefined;

const unresolved = (events: AdminSchemas["AlertEvent"][]) => events.filter((event) => event.state !== "resolved");

function useAlerts(session: Session) {
  return useInfiniteQuery({ ...alertEventsQuery, enabled: can(session, "sites.manage") });
}

function usePayments(session: Session) {
  const allowed = can(session, "sites.manage");
  const owner = useQuery({ ...accountQuery(session.account), enabled: allowed });
  const site = owner.data?.site_id;
  // A refund the payment channel froze for a decision, and a difference with the channel still under investigation.
  const refunds = useInfiniteQuery({ ...paymentRefundsQuery(site ?? "", "frozen"), enabled: Boolean(site) });
  const discrepancies = useInfiniteQuery({ ...discrepanciesQuery("open"), enabled: allowed });
  const frozen = firstPage(refunds.data);
  const open = firstPage(discrepancies.data);
  return frozen && open ? frozen.length + open.length : undefined;
}

export function usePending(): Partial<Record<Pending, number>> {
  const session = useSession();
  const alerts = firstPage(useAlerts(session).data);
  const approvals = firstPage(
    useInfiniteQuery({ ...approvalsQuery("pending"), enabled: can(session, "operations.approve") }).data,
  );
  const mappings = firstPage(
    useInfiniteQuery({ ...pendingMappingsQuery(), enabled: can(session, "suppliers.view") }).data,
  );
  return {
    alerts: alerts && unresolved(alerts).length,
    approvals: approvals?.length,
    payments: usePayments(session),
    mappings: mappings?.length,
  };
}

// The bell's alerts: the unresolved ones, newest first, the first four (adA_menu's bell); each opens on the alerts page.
export function useBell(href: (id: string) => string) {
  const session = useSession();
  const format = useFormat();
  const events = unresolved(firstPage(useAlerts(session).data) ?? []);
  return {
    count: events.length,
    items: events.slice(0, 4).flatMap((event) =>
      event.id
        ? [
            {
              key: event.id,
              title: event.kind ?? "",
              detail: event.fired_at ? format.dateTimeMinute(new Date(event.fired_at)) : "",
              href: href(event.id),
              severe: event.severity === "critical",
            },
          ]
        : [],
    ),
  };
}
