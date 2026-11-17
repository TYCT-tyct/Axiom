import { type ComponentType, createElement } from "react";
import { createRoot } from "react-dom/client";
import { boards } from "../src/demo.ts";
import "../src/demo-fonts.css";
import styles from "./main.module.css";

document.body.classList.add(styles.root as string);

const theme = new URLSearchParams(location.search).get("theme") === "dark" ? "深色" : "浅色";
const fonts = ["400 16px 'Axiom Sans'", "400 16px 'Axiom CJK'", "400 16px 'Axiom Mono'", "600 40px 'AX Serif'"];
await Promise.all(fonts.map((font) => document.fonts.load(font)));
const root = createRoot(document.getElementById("root") as HTMLElement);
const path = location.pathname;
// Load only this route before rendering: Suspense's reveal timer cannot run under the artboard's paused clock.
if (path.endsWith("/regions")) {
  const { regionDemo } = await import("./regions.tsx");
  root.render(await regionDemo(path.split("/")[1] as string, theme));
} else if (path === "/Login") {
  const { default: Login } = await import("../src/Login/Board.tsx");
  root.render(<Login mode={theme} />);
} else if (path === "/ClawdEgg") {
  const { default: ClawdEgg } = await import("../src/ClawdEgg/Board.tsx");
  root.render(<ClawdEgg mode={theme} />);
} else if (path !== "/AxApp" && path !== "/") {
  const load = boards[path.slice(1) as keyof typeof boards];
  if (!load) throw new Error(`no board ${path}`);
  const Board = (await load()) as ComponentType<Record<string, unknown>>;
  root.render(createElement(Board, { theme, mode: theme, system: theme }));
} else {
  const { default: AxApp } = await import("../src/AxApp/Board.tsx");
  root.render(
    <AxApp
      theme={theme}
      ref={(board) => {
        if (board) board.go("ov");
      }}
    />,
  );
}
