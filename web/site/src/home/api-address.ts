import { useTranslation } from "react-i18next";
import { useSite } from "./site-context";

// The address the reader's tools are set to: the site's API address (the board shows a placeholder), and with /v1 for
// the tools that speak the OpenAI interface. Until the site says it, or when it gives none, `known` is false and the
// text says it is unavailable.
export function useApiAddress() {
  const { t } = useTranslation();
  const api = useSite().brand?.api_base_url;
  const root = api ? api.replace(/\/+$/, "") : t("script.noApi");
  return { known: Boolean(api), root, v1: api ? `${root}/v1` : root };
}
