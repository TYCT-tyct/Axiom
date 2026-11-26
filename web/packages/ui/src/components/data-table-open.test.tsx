// @vitest-environment jsdom
import { createInstance } from "i18next";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { expect, it, vi } from "vitest";
import zh from "../locales/zh";
import { DataTable } from "./data-table";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type Row = { id: string; name: string };
const rows: Row[] = [
  { id: "a", name: "alpha" },
  { id: "b", name: "beta" },
  { id: "c", name: "gamma" },
  { id: "d", name: "delta" },
];

it("opens a row from its first cell or from the row, but not from a control inside the row", async () => {
  const i18n = createInstance();
  await i18n.init({ lng: "zh", defaultNS: "common", resources: { zh: { common: zh } } });
  const opened = vi.fn();
  const host = document.body.appendChild(document.createElement("div"));
  const root = createRoot(host);
  act(() =>
    root.render(
      <I18nextProvider i18n={i18n}>
        <DataTable<Row>
          label="rows"
          rows={rows}
          rowKey={(row) => row.id}
          columns={[
            { key: "name", header: "name", cell: (row) => row.name },
            { key: "control", header: "control", cell: (row) => <button type="button">{row.id}</button> },
          ]}
          onOpen={(row) => opened(row.id)}
          current="b"
        />
      </I18nextProvider>,
    ),
  );
  const body = [...host.querySelectorAll("tbody tr")];
  act(() => body[0]?.querySelector<HTMLElement>("td:first-child button")?.click());
  expect(opened).toHaveBeenLastCalledWith("a");
  act(() => body[2]?.querySelector<HTMLElement>("td:last-child")?.click());
  expect(opened).toHaveBeenLastCalledWith("c");
  opened.mockClear();
  act(() => body[3]?.querySelector<HTMLElement>("td:last-child button")?.click());
  expect(opened).not.toHaveBeenCalled();
  expect(body.map((row) => row.getAttribute("aria-selected"))).toEqual(["false", "true", "false", "false"]);
  act(() => root.unmount());
  host.remove();
});
