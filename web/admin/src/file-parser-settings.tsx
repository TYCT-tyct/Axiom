import { adminApi, unwrapChange } from "@axiom/api";
import { useQuery } from "@tanstack/react-query";
import { BackendSettings } from "./backend-settings";
import { fileParserSettingsQuery } from "./queries";

export function FileParserSettings() {
  const query = useQuery(fileParserSettingsQuery);
  return (
    <BackendSettings
      query={query}
      prefix="settings.fileParser"
      operation="file-parser-settings"
      save={(settings, reason, key) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/settings/file-parser", {
            params: { header: { "Idempotency-Key": key } },
            body: { settings, reason },
          }),
        )
      }
      test={(reason, key) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/settings/file-parser/tests", {
            params: { header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
    />
  );
}
