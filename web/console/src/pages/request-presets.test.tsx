// @vitest-environment jsdom
import type { Schemas } from "@axiom/api";
import { act } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { en } from "../locales/request-presets";
import { render, settle, setUpPages, submit } from "../testing";
import RequestPresetsPage from "./request-presets";

const api = vi.hoisted(() => ({ GET: vi.fn(), POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() }));
vi.mock("@axiom/api", async (original) => ({ ...(await original<typeof import("@axiom/api")>()), consoleApi: api }));
vi.mock("@axiom/app", async (original) => (await import("../testing")).everyPermission(original));

const saved = {
  slug: "bot",
  name: "Bot",
  config: { model: "vendor/model", system: "Initial prompt", temperature: 0, max_output_tokens: 100, top_p: 0.5 },
} satisfies Schemas["Preset"];
const listPath = "/api/console/v1/presets";
const detailPath = "/api/console/v1/presets/{slug}";

function response(status: number, data?: unknown) {
  return Promise.resolve(
    status < 400
      ? { data, response: new Response(null, { status }) }
      : { error: { error: { code: "internal", message: "" } }, response: new Response(null, { status }) },
  );
}

async function click(element: HTMLElement | null) {
  if (!element) throw new Error("Missing click target");
  await act(async () => element.click());
  await settle();
}

function field(container: HTMLElement, name: string) {
  const input = container.querySelector<HTMLInputElement | HTMLTextAreaElement>(`dialog [name=${name}]`);
  if (!input) throw new Error(`Missing input ${name}`);
  return input;
}

setUpPages({ en: { translation: { requestPresets: { copy: en } } } });
beforeEach(() => {
  vi.clearAllMocks();
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  HTMLDialogElement.prototype.close = function () {
    this.open = false;
  };
});

describe("request presets", () => {
  it("keeps a refused create draft and shows the returned configuration when retry succeeds", async () => {
    const newPreset = { slug: "new_bot", name: "New bot", config: { temperature: 0.4, system: "Keep it short" } };
    api.GET.mockResolvedValueOnce(await response(200, { data: [], total_count: 0 }));
    api.POST.mockImplementationOnce(() => response(409)).mockImplementationOnce(() => response(201, newPreset));
    api.GET.mockImplementation(() => response(200, { data: [newPreset], total_count: 1 }));
    const { container } = await render(<RequestPresetsPage />);
    await click(container.querySelector(".qpa button"));
    field(container, "name").value = "New bot";
    field(container, "slug").value = "new_bot";
    field(container, "temperature").value = "0.4";
    field(container, "system").value = "Keep it short";
    const form = container.querySelector("form") as HTMLFormElement;
    await submit(form);
    expect(container.querySelector("[role=alert]")?.textContent).toContain("Couldn’t save");
    expect(field(container, "name").value).toBe("New bot");
    expect(field(container, "system").value).toBe("Keep it short");
    expect(field(container, "temperature").value).toBe("0.4");
    await submit(form);
    expect(api.POST).toHaveBeenLastCalledWith(listPath, { body: newPreset });
    expect(container.querySelector("dialog")).toBeNull();
    expect(container.querySelector(".qpre")?.textContent).toContain("New bot · @preset/new_bot");
  });

  it("loads the detail and replaces the config while preserving the reference and other API fields", async () => {
    const replacement = {
      name: "Help bot",
      config: { ...saved.config, system: "Updated prompt", max_output_tokens: 300 },
    };
    let listed: Schemas["Preset"] = { ...saved, config: {} };
    api.GET.mockImplementation((path) =>
      path === detailPath ? response(200, saved) : response(200, { data: [listed], total_count: 1 }),
    );
    api.PUT.mockImplementation(() => {
      listed = { slug: saved.slug, ...replacement };
      return response(200, listed);
    });
    const { container } = await render(<RequestPresetsPage />);
    await click(container.querySelector(".qpre"));
    expect(field(container, "system").value).toBe("Initial prompt");
    field(container, "name").value = "Help bot";
    field(container, "system").value = "Updated prompt";
    field(container, "max").value = "300";
    await submit(container.querySelector("form") as HTMLFormElement);
    expect(api.PUT).toHaveBeenCalledWith(detailPath, { params: { path: { slug: "bot" } }, body: replacement });
    expect(container.querySelector(".qpre")?.textContent).toContain("Help bot · @preset/bot");
  });

  it("only deletes after confirmation and removes the confirmed item from the list", async () => {
    const data = [saved];
    api.GET.mockImplementation((path) =>
      response(200, path === detailPath ? saved : { data, total_count: data.length }),
    );
    api.DELETE.mockImplementation(() => {
      data.pop();
      return response(204);
    });
    const { container } = await render(<RequestPresetsPage />);
    await click(container.querySelector(".qpre"));
    const remove = container.querySelector<HTMLButtonElement>("dialog .nkfl");
    await click(remove);
    expect(remove?.textContent).toBe("Confirm deletion");
    expect(api.DELETE).not.toHaveBeenCalled();
    await click(remove);
    expect(api.DELETE).toHaveBeenCalledExactlyOnceWith(detailPath, { params: { path: { slug: "bot" } } });
    expect(container.querySelector(".qpre")).toBeNull();
    expect(container.textContent).toContain("No request presets yet");
    expect(document.activeElement).toBe(container.querySelector("h1"));
  });
});
