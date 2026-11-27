import { loadTranslatedPage } from "../locales/load-page";
import { modelStatus } from "../models/catalog";
import { tabOf } from "../tab-of";
import { movedPage, shellPage, text } from "./shell";

// 渠道与模型: the upstreams, models, routing, pricing and plugins.
export const supplyRoutes = [
  shellPage(
    "/suppliers",
    () => loadTranslatedPage(import("../pages/suppliers")),
    (search) => ({
      tab: tabOf(search.tab, ["models", "inbox", "keys"] as const),
      credential: text(search.credential),
    }),
  ),
  shellPage(
    "/suppliers/$supplierId",
    () => loadTranslatedPage(import("../pages/supplier")),
    (search) => ({
      tab: tabOf(search.tab, ["models", "keys", "sync", "settings"] as const),
      endpoint: text(search.endpoint),
      credential: text(search.credential),
    }),
  ),
  // The queue of unmapped names became 上游's 待处理.
  movedPage("/mappings", () => ({ to: "/suppliers", search: { tab: "inbox" } })),
  shellPage(
    "/models",
    () => import("../pages/models"),
    (search) => ({
      tab: tabOf(search.tab, ["authors"] as const),
      status: modelStatus(search.status),
      author: text(search.author),
      q: text(search.q),
      model: text(search.model),
    }),
  ),
  shellPage(
    "/routing",
    () => loadTranslatedPage(import("../pages/routing")),
    (search) => ({ tab: tabOf(search.tab, ["defaults", "sources"] as const), preset: text(search.preset) }),
  ),
  shellPage(
    "/pricing",
    () => loadTranslatedPage(import("../pages/pricing")),
    (search) => ({ tab: tabOf(search.tab, ["rules", "rates", "policy"] as const), price: text(search.price) }),
  ),
  // The pages the design folded into tabs and drawers keep their addresses: a link, a bookmark or the bell's old
  // target lands on the tab or the open row that has them now.
  movedPage("/models/$modelId", ({ modelId = "" }) => ({ to: "/models", search: { model: modelId } })),
  movedPage("/routing-presets", () => ({ to: "/routing" })),
  movedPage("/source-classes", () => ({ to: "/routing", search: { tab: "sources" } })),
  movedPage("/exchange-rates", () => ({ to: "/pricing", search: { tab: "rates" } })),
  shellPage("/plugins", () => loadTranslatedPage(import("../pages/plugins"))),
  shellPage("/plugins/web", () => loadTranslatedPage(import("../pages/plugins"))),
  shellPage("/plugins/file", () => loadTranslatedPage(import("../pages/plugins"))),
] as const;
