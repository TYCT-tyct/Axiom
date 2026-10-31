import type { AdminSchemas } from "@axiom/api";

// Whether an endpoint can be called, and so tested, as far as the supplier's page can tell: a switched-off endpoint or
// one whose cost is not known is out of routing, and the server would refuse the test before paying for anything.
// "ready" is not a promise: the node may still hold no free key for it.
export function endpointState(endpoint: AdminSchemas["SupplierEndpoint"]) {
  if (!endpoint.enabled) {
    return "off";
  }
  return endpoint.cost_state === "known" ? "ready" : endpoint.cost_state;
}

// The formats of an endpoint a test can call: the generation formats (api/admin.yaml TestFormat). Embeddings and rerank
// are listed on an endpoint but have no test yet, so an endpoint that serves only them offers none.
export function testFormats(formats: AdminSchemas["ModelFormat"][]): AdminSchemas["TestFormat"][] {
  return formats.filter((f): f is AdminSchemas["TestFormat"] => f === "messages" || f === "responses" || f === "chat");
}

// A duration as a person reads it: milliseconds below a second, seconds to a tenth above.
export function duration(ms: number): { unit: "ms" | "s"; value: number } {
  return ms < 1000 ? { unit: "ms", value: ms } : { unit: "s", value: Math.round(ms / 100) / 10 };
}
