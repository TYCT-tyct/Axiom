import type { Schemas } from "@axiom/api";

// mergeOrder keeps what the created answer alone has, the channel's client secret, and takes the state from the newer
// read. A read of a different order is not merged, and a checkout only exists when one of the answers actually had one:
// an empty object would read as "the channel confirmed" and hide the warning before payment.
export function mergeOrder(created: Schemas["Order"] | null, live: Schemas["Order"] | undefined) {
  if (!created) {
    return live;
  }
  if (!live || created.id !== live.id) {
    return created;
  }
  const merged = { ...created, ...live };
  if (!created.checkout && !live.checkout) {
    return merged;
  }
  return { ...merged, checkout: { ...live.checkout, ...created.checkout } };
}
