import { range, type Schemas, uuid } from "./admin-fixtures.ts";

// A call's route as routing stores it (CallRoute): more candidates left out than are named, and, for a failed call, the
// endpoint that failed before the one that answered.
export const callRoute = (failed: boolean): Schemas["CallRoute"] => ({
  preset: { id: uuid(19, 1), version: 3 },
  overrides: ["sort", "max_price"],
  excluded: range(16).map((i) => ({ endpoint: uuid(6, 10 + i), reason: i % 2 ? "no_price" : "breaker_open" })),
  excluded_more: 4,
  excluded_reasons: { no_price: 10, breaker_open: 10 },
  route: [uuid(6, 1), uuid(6, 2)],
  attempts: failed
    ? [{ endpoint: uuid(6, 1), failed: true, scope: 2 }, { endpoint: uuid(6, 2) }]
    : [{ endpoint: uuid(6, 1) }],
});
