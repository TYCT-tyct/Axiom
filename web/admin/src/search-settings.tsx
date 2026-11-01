import { adminApi, unwrapChange } from "@axiom/api";
import { useQuery } from "@tanstack/react-query";
import { BackendSettings } from "./backend-settings";
import { searchSettingsQuery } from "./queries";

export function SearchSettings() {
  const query = useQuery(searchSettingsQuery);
  return (
    <BackendSettings
      query={query}
      prefix="settings.search"
      operation="search-settings"
      save={(settings, reason, key) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/settings/search", {
            params: { header: { "Idempotency-Key": key } },
            body: { settings, reason },
          }),
        )
      }
      test={(reason, key) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/settings/search/tests", {
            params: { header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
    />
  );
}
