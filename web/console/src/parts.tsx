// A key's text is never shown again after it is made; the list shows where it starts and ends.
export const maskedKey = (key: { prefix: string; last4: string }) => `${key.prefix}…${key.last4}`;

export { toInstant, toLocalInput } from "@axiom/app";
