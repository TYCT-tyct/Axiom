import type { AdminState, Schemas } from "./admin-data.ts";
import { uuid } from "./admin-data.ts";
import { base, type Ctx, changes, reads, route } from "./admin-route.ts";
import { fail, type Handler, ok, paged } from "./mock.ts";

// The catalog: authors, models, their names, specs and reference prices. A model is changed as of the revision the
// caller read, and a stale one is refused instead of overwritten.

type Model = Schemas["Model"];

const find = (state: AdminState, id: string | undefined) => state.models.find((m) => m.id === id);
const summary = ({ id, slug, author, status, version_basis, price_source, rev }: Model) => ({
  id,
  slug,
  author,
  status,
  version_basis,
  price_source,
  rev,
});

function listModels({ query, state }: Ctx<"/api/admin/v1/models", "get">) {
  const status = query.get("status");
  const author = query.get("author");
  const prefix = query.get("slug_prefix") ?? "";
  const rows = state.models
    .filter((m) => (!status || m.status === status) && (!author || m.author === author) && m.slug.startsWith(prefix))
    .map(summary);
  const page = paged(rows, query);
  return page ? ({ status: 200, json: page } as const) : fail(400, "invalid_request");
}

function createModel({ body, state }: Ctx<"/api/admin/v1/models", "post">) {
  const author = body.slug.split("/")[0] ?? "";
  if (!state.authors.some((a) => a.slug === author)) {
    return fail(422, "unprocessable");
  }
  if (state.models.some((m) => m.slug === body.slug)) {
    return fail(409, "name_taken");
  }
  const model: Model = {
    id: uuid(5, state.models.length + 100),
    slug: body.slug,
    author,
    status: "draft",
    version_basis: body.version_basis,
    price_source: body.price_source,
    rev: 0,
    spec: body.spec,
    names: [],
    prices: [],
  };
  state.models.unshift(model);
  return { status: 201, json: model } as const;
}

// The shared start of a change as of a revision: the model must exist, be the revision the caller read, and not be retired.
function target(state: AdminState, id: string | undefined, rev: number | undefined) {
  const model = find(state, id);
  if (!model) {
    return { refused: fail(404, "not_found") };
  }
  if (rev !== undefined && rev !== model.rev) {
    return { refused: fail(409, "stale_revision") };
  }
  return model.status === "retired" ? { refused: fail(409, "conflict") } : { model };
}

function updateSpec({ body, params, state }: Ctx<"/api/admin/v1/models/{model_id}/spec", "put">) {
  const { model, refused } = target(state, params.model_id, body.rev);
  if (!model) {
    return refused;
  }
  model.spec = body.spec;
  model.rev += 1;
  return ok(model);
}

function addName({ body, params, state }: Ctx<"/api/admin/v1/models/{model_id}/names", "post">) {
  const { model, refused } = target(state, params.model_id, undefined);
  if (!model) {
    return refused;
  }
  if (state.models.some((m) => m.names.some((n) => n.name.toLowerCase() === body.name.toLowerCase()))) {
    return fail(409, "name_taken");
  }
  model.names.push({ name: body.name, visibility: body.visibility });
  return ok(model);
}

// The review gate: what a draft must have before it goes on sale.
function ready(model: Model) {
  const priced = model.prices.filter((p) => p.schedule);
  const items = model.spec.items ?? [];
  return (
    Array.isArray(model.spec.items) &&
    Array.isArray(model.spec.tiers) &&
    model.names.some((n) => n.name === model.slug && n.visibility === "public") &&
    priced.length > 0 &&
    priced.every((p) => items.every((item) => p.schedule?.rates[item] !== undefined))
  );
}

function setStatus({ body, params, state }: Ctx<"/api/admin/v1/models/{model_id}/status", "put">) {
  const { model, refused } = target(state, params.model_id, body.rev);
  if (!model) {
    return refused;
  }
  const legal = body.status === "listed" ? model.status === "draft" : model.status === "listed";
  if (!legal) {
    return fail(409, "conflict");
  }
  if (body.status === "listed" && !ready(model)) {
    return fail(422, "not_ready");
  }
  model.status = body.status;
  model.rev += 1;
  return ok(model);
}

function publishPrice({ body, params, state }: Ctx<"/api/admin/v1/models/{model_id}/prices", "post">) {
  const { model, refused } = target(state, params.model_id, undefined);
  if (!model) {
    return refused;
  }
  const current = model.prices.find((p) => p.currency === body.schedule.currency);
  if (body.source !== model.price_source) {
    return fail(422, "unprocessable");
  }
  if (current?.id !== body.base) {
    return fail(409, "stale_revision");
  }
  const row = {
    id: uuid(6, model.prices.length + state.stored.size + 100),
    currency: body.schedule.currency,
    source: body.source,
    schedule: body.schedule,
    published_at: new Date().toISOString(),
  };
  model.prices = [...model.prices.filter((p) => p !== current), row];
  return ok(model);
}

function selectSource({ body, params, state }: Ctx<"/api/admin/v1/models/{model_id}/price-source", "put">) {
  const { model, refused } = target(state, params.model_id, body.rev);
  if (!model) {
    return refused;
  }
  const stale = model.prices.some((p) => body.bases[p.currency] !== p.id);
  if (stale) {
    return fail(409, "stale_revision");
  }
  model.price_source = body.source;
  model.prices = body.prices.map((p, i) => ({
    id: uuid(6, state.stored.size + i + 200),
    currency: p.schedule.currency,
    source: body.source,
    schedule: p.schedule,
    published_at: new Date().toISOString(),
  }));
  model.rev += 1;
  return ok(model);
}

function saveAuthor({ body, params, state }: Ctx<"/api/admin/v1/authors/{slug}", "put">) {
  const author = { slug: params.slug ?? "", name: body.name };
  state.authors = [...state.authors.filter((a) => a.slug !== author.slug), author].sort((a, b) =>
    a.slug.localeCompare(b.slug),
  );
  return ok(author);
}

export const catalogHandlers: Handler<AdminState>[] = [
  route("get", `${base}/authors`, reads("catalog.view"), ({ state }) => ok({ data: state.authors })),
  route("put", `${base}/authors/{slug}`, changes("catalog.manage"), saveAuthor),
  route("get", `${base}/models`, reads("catalog.view"), listModels),
  route("post", `${base}/models`, changes("catalog.manage"), createModel),
  route("get", `${base}/models/{model_id}`, reads("catalog.view"), ({ params, state }) => {
    const model = find(state, params.model_id);
    return model ? ok(model) : fail(404, "not_found");
  }),
  route("put", `${base}/models/{model_id}/spec`, changes("catalog.manage"), updateSpec),
  route("post", `${base}/models/{model_id}/names`, changes("catalog.manage"), addName),
  route("put", `${base}/models/{model_id}/status`, changes("catalog.manage"), setStatus),
  route("post", `${base}/models/{model_id}/prices`, changes("catalog.manage"), publishPrice),
  route("put", `${base}/models/{model_id}/price-source`, changes("catalog.manage"), selectSource),
];
