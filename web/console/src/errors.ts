import { ApiError } from "@axiom/api";
import { useTranslation } from "react-i18next";

// What a failed call tells the user. The server's message is English text for logs; a page translates the error code
// instead (the contract's closed list, so a new code stops the build until it has words).
export function useErrors() {
  const { t } = useTranslation();
  return {
    message(error: unknown) {
      if (!(error instanceof ApiError)) {
        return t("errors.unknown");
      }
      if (error.code === undefined) {
        return error.status === 0 ? t("errors.network") : t("errors.unknown");
      }
      return t(`errors.${error.code}`, { seconds: error.retryAfter ?? 60 });
    },
    // Whether the server found this field of the request wrong: the control is marked, and the form's message above
    // says what to do.
    field(error: unknown, name: string) {
      const named = error instanceof ApiError && error.fields.some(({ pointer }) => isField(pointer, name));
      return named ? t("errors.field") : undefined;
    },
  };
}

function isField(pointer: string, name: string) {
  return pointer === name || pointer === `/${name}` || pointer.startsWith(`/${name}/`);
}
