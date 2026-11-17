import { type Component, type ComponentType, createElement } from "react";
import { slices } from "../src/demo.ts";

type Values = Record<string, unknown>;
type Logic = Component<Values, Values> & { renderVals(): Values; go(page: string): void };
type Scene = {
  logic(): Promise<new (props: Values) => Logic>;
  Shell(): Promise<ComponentType<Values>>;
  Overview(): Promise<ComponentType<Values>>;
  ShellData(): Promise<(t: (key: string) => string) => Values>;
  OverviewData(): Promise<(t: (key: string) => string) => Values>;
  ShellZH(): Promise<Record<string, string>>;
  OverviewZH(): Promise<Record<string, string>>;
};

export async function regionDemo(board: string, theme: string) {
  const scene = (slices as unknown as Record<string, Scene>)[board];
  if (!scene) throw new Error(`no region demo for ${board}`);
  const [Source, Shell, Overview, shellData, overviewData, shellZH, overviewZH] = await Promise.all([
    scene.logic(),
    scene.Shell(),
    scene.Overview(),
    scene.ShellData(),
    scene.OverviewData(),
    scene.ShellZH(),
    scene.OverviewZH(),
  ]);
  const shellProps = shellData((key) => shellZH[key] as string);
  const overviewProps = overviewData((key) => overviewZH[key] as string);
  // Only this reference route uses the board's demo controller. Region imports have no edge to it.
  class Demo extends Source {
    override componentDidMount() {
      super.componentDidMount?.();
      this.go("ov");
    }
    override render() {
      const values = this.renderVals();
      return createElement(Shell, { ...shellProps, values }, createElement(Overview, { ...overviewProps, values }));
    }
  }
  return createElement(Demo, { theme });
}
