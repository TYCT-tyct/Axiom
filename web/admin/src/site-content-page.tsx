import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { DataState, ForbiddenState, PageHead } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { PageBody } from "./parts";
import { accountQuery } from "./queries";

// Site content belongs to the site of the operator's account, as on the site settings page. Whoever may not see it
// (permission, by default the site's own) is told so.
export function SiteContentPage({
  title,
  description,
  actions,
  permission = "sites.manage",
  children,
}: {
  title: string;
  description: string;
  actions?: ReactNode;
  permission?: string;
  children: (siteId: AdminSchemas["SiteId"], currency: string) => ReactNode;
}) {
  const session = useSession();
  const allowed = can(session, permission);
  const account = useQuery({ ...accountQuery(session.account), enabled: allowed });
  return (
    <PageBody>
      <PageHead title={title} lede={description} actions={actions} />
      {allowed ? (
        <DataState query={account}>{(data) => children(data.site_id, data.balance.balance.currency)}</DataState>
      ) : (
        <ForbiddenState />
      )}
    </PageBody>
  );
}
