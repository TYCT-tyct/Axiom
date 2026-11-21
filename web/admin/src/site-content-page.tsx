import { can, useSession } from "@axiom/app";
import { DataState, ForbiddenState, PageHead } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { PageBody } from "./parts";
import { accountQuery } from "./queries";

// Site content belongs to the site of the operator's account, as on the site settings page.
export function SiteContentPage({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: (siteId: string) => ReactNode;
}) {
  const session = useSession();
  const allowed = can(session, "sites.manage");
  const account = useQuery({ ...accountQuery(session.account), enabled: allowed });
  return (
    <>
      <PageHead title={title} lede={description} />
      <PageBody>
        {allowed ? <DataState query={account}>{(data) => children(data.site_id)}</DataState> : <ForbiddenState />}
      </PageBody>
    </>
  );
}
