import { useTranslation } from "react-i18next";
import { useSiteBrand } from "../shell";

// The address the reader's tools are set to: the site's API address (the board shows a placeholder), and with /v1 for
// the tools that speak the OpenAI interface. Until the site says it, or when it gives none, the text says it is
// unavailable.
export function useApiAddress() {
  const { t } = useTranslation();
  const api = useSiteBrand()?.api_base_url;
  const root = api ? api.replace(/\/+$/, "") : t("script.noApi");
  return { root, v1: api ? `${root}/v1` : root };
}
