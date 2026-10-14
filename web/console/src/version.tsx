import { consoleApi, unwrap } from "@axiom/api";
import { Button, Notice } from "@axiom/ui";
import { queryOptions, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

// The running program's version, asked again every few minutes and whenever the tab regains focus.
export const versionQuery = queryOptions({
  queryKey: ["version"],
  queryFn: () => unwrap(consoleApi.GET("/api/console/v1/version")),
  refetchInterval: 5 * 60_000,
  refetchOnWindowFocus: "always",
});

// NewVersionNotice offers a reload once the program answering differs from the one this page was built for, so a page
// left open across a deployment stops calling an API it may no longer match. A build without a version (the dev
// server) never asks.
export function NewVersionNotice() {
  const { t } = useTranslation();
  const { data } = useQuery({ ...versionQuery, enabled: __AXIOM_VERSION__ !== "" });
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
