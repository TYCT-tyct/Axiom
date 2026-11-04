import type { AdminSchemas } from "@axiom/api";

type Settings = AdminSchemas["SiteSettings"];
type View = AdminSchemas["SiteSettingsView"];

// modelPricesChangeUnconfirmed says the caller changed the switch but the server's answer does not confirm the value.
// A page read from a new node may send its PUT to an old one: the old decoder ignores the field and answers 200 with a
// view that has no model_prices_public, so reporting success would leave prices public after the person chose private.
export function modelPricesChangeUnconfirmed(read: View, asked: Settings, saved: View): boolean {
  const initial = read.settings.model_prices_public ?? read.defaults.model_prices_public;
  if (asked.model_prices_public === undefined || initial === undefined || asked.model_prices_public === initial) {
    return false;
  }
  const stored = saved.settings.model_prices_public ?? saved.defaults.model_prices_public;
  return stored !== asked.model_prices_public;
}
