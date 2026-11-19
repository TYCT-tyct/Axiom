import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { useEffect, useState } from "react";

type Models = Schemas["PublicModelList"];

// The site's published models (listPublicModels), asked for once for the page: the models screen shows them and the sea
// bed's footer learns from them whether the site publishes its prices. A failed call is forgotten, so the next ask
// tries again.
let asked: Promise<Models> | undefined;
export function publicModels(): Promise<Models> {
  asked ??= unwrap(consoleApi.GET("/api/console/v1/models"));
  const call = asked;
  call.catch(() => {
    if (asked === call) asked = undefined;
  });
  return call;
}

// Whether the site publishes its prices: listPublicModels leaves every pricing out when it keeps them for its users.
// "unknown" until the models have come, if they never do, or when there are none to tell by.
export type Prices = "public" | "members" | "unknown";
const pricesOf = (models: Models): Prices => {
  if (models.data.length === 0) return "unknown";
  const priced = models.data.some((m) => m.pricing !== undefined || (m.endpoints ?? []).some((e) => e.pricing));
  return priced ? "public" : "members";
};

export function usePrices(): Prices {
  const [prices, setPrices] = useState<Prices>("unknown");
  useEffect(() => {
    let live = true;
    publicModels().then(
      (models) => live && setPrices(pricesOf(models)),
      () => {},
    );
    return () => {
      live = false;
    };
  }, []);
  return prices;
}
