import { InlineError } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { useErrors } from "./errors";

export { LoadMore, Stat } from "@axiom/ui";

// Signing out that did not happen leaves the session open; the reason and that fact are said next to the button, which
// stays there to try again.
export function SignOutError({ error }: { error: unknown }) {
  const { t } = useTranslation("common");
  const errors = useErrors();
  return error ? <InlineError>{t("signOutFailed", { reason: errors.message(error) })}</InlineError> : null;
}
