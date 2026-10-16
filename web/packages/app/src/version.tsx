import { Button, Notice } from "@axiom/ui";
import { queryOptions, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useAuth } from "./session";

// NewVersionNotice offers a reload once the program answering differs from the one this page was built for, so a page
// left open across a deployment stops calling an API it may no longer match. A build without a version (the dev
// server) never asks. The version is asked again every few minutes and whenever the tab regains focus.
export function NewVersionNotice() {
  const { t } = useTranslation("common");
  const { api } = useAuth();
  const { data } = useQuery({ ...versionQuery(api.version), enabled: __AXIOM_VERSION__ !== "" });
  if (data === undefined || data.version === __AXIOM_VERSION__) {
    return null;
  }
  return (
    <Notice
      action={
        <Button size="sm" variant="primary" onClick={() => window.location.reload()}>
          {t("newVersion.reload")}
        </Button>
      }
    >
      {t("newVersion.message")}
    </Notice>
  );
}

// Exported for the test that the call stops once nobody watches the query.
export function versionQuery(version: (signal: AbortSignal) => Promise<{ version: string }>) {
  return queryOptions({
    queryKey: ["version"],
    queryFn: ({ signal }) => version(signal),
    refetchInterval: 5 * 60_000,
    refetchOnWindowFocus: "always",
  });
}
