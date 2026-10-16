export default {
  description:
    "Model names suppliers list that the catalog doesn’t recognise. Until mapped they get no endpoint and take no traffic. The oldest come first.",
  empty: { title: "Nothing waiting to be mapped", body: "The catalog recognises every model name the suppliers list." },
  anySupplier: "All suppliers",
  col: { name: "Upstream model name", supplier: "Supplier", formats: "Formats" },
  map: "Map",
  dialog: {
    title: "Map a model name",
    body: "Which model is this name? The upstream name:",
    action: "Map",
    model: "Model it means",
    scope: "Scope",
    scopeGroup: "This group only ({{group}})",
    scopeSupplier: "The whole supplier",
    modelMissing: "Choose a model that is in the catalog, from the suggestions.",
    done: "Mapped “{{name}}”.",
    wait: "The endpoint it makes waits for the authenticity check before it takes traffic. A mapping is never changed; a wrong one is corrected by a new mapping in a narrower scope.",
  },
};
