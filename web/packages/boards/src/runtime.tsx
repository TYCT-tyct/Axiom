import { Component, type ReactNode } from "react";

type Binding = {
  node: Element | null;
  applied: Element | null;
  receive: (node: Element) => void;
  ref: (node: Element | null) => void;
};
type Bind = (key: string, receive: Binding["receive"]) => Binding["ref"];
type Values = Record<string, unknown>;

export class DCLogic extends Component<Values, Values> {
  override state: Values = {};
  private bindings = new Map<string, Binding>();
  static template: (values: Values, bind: Bind) => ReactNode;

  private bind: Bind = (key, receive) => {
    let binding = this.bindings.get(key);
    if (!binding) {
      binding = {
        node: null,
        applied: null,
        receive,
        ref: (node) => {
          if (binding) binding.node = node;
        },
      };
      this.bindings.set(key, binding);
    }
    binding.receive = receive;
    return binding.ref;
  };

  // React commits child refs first. The design runtime binds in template order, so parents must precede engines.
  flushRefs() {
    for (const binding of this.bindings.values()) {
      if (binding.node && binding.node !== binding.applied) {
        binding.applied = binding.node;
        binding.receive(binding.node);
      }
    }
  }

  renderVals(): Values {
    return {};
  }
  override render() {
    return (this.constructor as typeof DCLogic).template(this.renderVals(), this.bind);
  }
}
