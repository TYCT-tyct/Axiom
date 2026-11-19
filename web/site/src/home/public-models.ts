import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { useEffect, useState } from "react";

type Models = Schemas["PublicModelList"];

// The site's published models (listPublicModels), one answer for the page that every screen reads: the models screen
// shows them, and the sea bed's footer learns from them whether the site publishes its prices. The answer carries when
// it came, so a screen built again later (the layout changed) shows the time of the list it shows; a retry, from any
// screen, replaces it for all of them.
export type Answer =
  | { status: "pending" }
  | { status: "failed"; error: unknown; at: number }
  | { status: "ready"; models: Models; at: Date };

let answer: Answer = { status: "pending" };
let asking = false;
const listeners = new Set<(answer: Answer) => void>();

function settle(next: Answer) {
  answer = next;
  for (const listener of [...listeners]) listener(answer);
}

// ask asks for the models unless they are on their way or have come; after a failure it asks again.
export function ask() {
  if (asking || answer.status === "ready") return;
  asking = true;
  if (answer.status === "failed") settle({ status: "pending" });
  unwrap(consoleApi.GET("/api/console/v1/models")).then(
    (models) => {
      asking = false;
      settle({ status: "ready", models, at: new Date() });
    },
    (error: unknown) => {
      asking = false;
      settle({ status: "failed", error, at: Date.now() });
    },
  );
}

// subscribe calls listener with the answer now and with every later one, asking for the models unless they have come
// (a screen arriving after a failure asks again, as a retry); the returned function stops it.
export function subscribe(listener: (answer: Answer) => void) {
  listeners.add(listener);
  listener(answer);
  ask();
  return () => {
    listeners.delete(listener);
  };
}

// Whether the site publishes its prices: listPublicModels leaves every pricing out when it keeps them for its users.
// "unknown" until the models have come, if they never do, or when there are none to tell by.
export type Prices = "public" | "members" | "unknown";
const pricesOf = (current: Answer): Prices => {
  if (current.status !== "ready" || current.models.data.length === 0) return "unknown";
  const priced = current.models.data.some((m) => m.pricing !== undefined || (m.endpoints ?? []).some((e) => e.pricing));
  return priced ? "public" : "members";
};

export function usePrices(): Prices {
  const [prices, setPrices] = useState<Prices>("unknown");
  useEffect(() => subscribe((current) => setPrices(pricesOf(current))), []);
  return prices;
}
