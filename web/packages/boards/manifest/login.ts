import type { Region } from "../regions.ts";

// The console's sign-in pages (#747 S3, port design §9): the Login board's frame, and the parts of the steps it draws,
// which the pages compose into the steps the product has (email, password, two-step verification, consent). The form
// is drawn by login-ui.js, so each cut runs it. LoginPhone and LoginDark are the same board at a phone's width and in
// the dark scheme (prop mode), with the same markup and CSS: one cut serves all three.
type Scene = NonNullable<Region["render"]>;
const scene = (props: Record<string, string> = {}): Scene => ({
  root: ".lg",
  actions: [],
  props,
  engines: ["login-ui"],
});
const start = scene();
const emailError = scene({ error: "邮箱格式不对" });
const consentError = scene({ error: "没勾选同意" });
const code = scene({ step: "验证码" });
const h = (name: string) => `{{${name}}}`;
// The English of the drawn words a part keeps only in its demo (the pages put their own words in its slots and texts).
const drawn: Record<string, string> = {
  m4124d22fb3: "Sign in or sign up",
  m4f9d29981a: "S",
  m72726d8818: "中文",
  m73075237fd: "Email",
  m75fd91219e: "That email doesn’t look right. Check it.",
  m7c9691192f: "Continue",
  ma2123d6950: "or",
  me44c5a8272: "Continue with GitHub",
  m5c778e520a: "I have read and agree to the Terms of Service and the Privacy Policy",
  me7459a5c06: "Use another email",
};

const part = (
  name: string,
  root: string,
  render: Scene,
  more: Omit<Region, "board" | "name" | "roots"> = {},
): Region => ({
  board: "Login",
  name,
  roots: [root],
  render,
  ...more,
  english: { ...drawn, ...more.english },
});

// A text field as drawn (the email step's), and one for any other text: the converter types inputmode only as the
// board writes it, so each keeps its own.
const field = (name: string, inputMode: string): Region =>
  part(name, ".lg-field", emailError, {
    roots: [".lg-label", ".lg-field"],
    text: { label: ".lg-label" },
    elements: {
      ".lg-label": { set: { for: h("id") } },
      ".lg-field": { set: { class: `lg-field ${h("fieldCls")}` } },
      ".lg-field input": {
        set: {
          id: h("id"),
          name: h("name"),
          type: h("type"),
          inputmode: inputMode,
          autocomplete: h("autoComplete"),
          placeholder: h("placeholder"),
          value: h("value"),
          spellcheck: "false",
          "aria-describedby": h("describedBy"),
          oninput: h("onInput"),
          ref: h("input"),
        },
      },
    },
    classes: { fieldCls: ["bad"] },
  });
const note = ".lg-row > .lg-note:last-child";

/** @public loaded by regions.ts from the directory listing */
export default [
  part("LoginShell", ".lg", start, {
    // the footer's contact link is not built (port design §9, O21)
    omit: [".lg-foot"],
    children: ".lg-form",
    slots: { brand: ".lg-sea .brand", languages: ".lg-top" },
    text: { pitch: ".lg-pitch .h", lede: ".lg-pitch .s" },
    attributes: { home: { selector: ".lg-sea .brand", attribute: "href" } },
    elements: {
      // the site's brand reaches the focus ring and the accent (port design R10: not the primary button)
      ".lg": { set: { class: `lg ${h("modeCls")}`, lang: h("lang"), style: h("vars") } },
      "canvas.sea": { set: { ref: h("seaCanvas") } },
      ".lg-sea .brand": { set: { style: h("brandStyle") } },
      ".lg-pitch .kern": { unless: "noKern" },
      ".lg-side": { set: { id: "main", tabindex: "-1" } },
    },
    classes: { modeCls: ["light", "dark"] },
    english: {
      m0d0cee5072: "Every route is tested. You pay for the route that served you.",
      m9f6fee1aba: "Language",
      md9aebc95f7: "中文EN",
      me1dbc1c99f: "One ",
      m127a0875a8: " for every model",
    },
  }),
  // The board's mark beside the site's name, where the site has no logo of its own.
  { board: "Login", name: "LoginMark", roots: [".lg-sea .brand > svg"] },
  // The site's name beside its mark, which a long name must not run under the language switch with.
  part("LoginBrandName", ".lg-sea .brand > span", start, {
    text: { name: ".lg-sea .brand > span" },
    elements: { ".lg-sea .brand > span": { set: { style: h("style") } } },
  }),
  // The language switch: the page's own language, marked current, and the other one.
  part("LoginLanguage", ".lg-top a:first-child", start, {
    text: { label: ".lg-top a:first-child" },
  }),
  part("LoginOtherLanguage", ".lg-top a:last-child", start, {
    text: { label: ".lg-top a:last-child" },
    elements: { ".lg-top a:last-child": { set: { onclick: h("onPress") } } },
  }),
  part("LoginStep", ".lg-step", start, {
    children: ".lg-step",
    elements: { ".lg-step": { set: { class: `lg-step ${h("stepCls")}`, "data-step": h("shape") } } },
    classes: { stepCls: ["in"] },
  }),
  part("LoginDecode", ".lg-dc", start, { elements: { ".lg-dc": { set: { ref: h("canvas") } } } }),
  part("LoginTitle", ".lg-title", start, {
    children: ".lg-title",
    attributes: { label: { selector: ".lg-title", attribute: "aria-label" } },
    elements: { ".lg-title": { set: { ref: h("title") } } },
  }),
  part("LoginChar", ".lg-title .ch:first-child", start, { text: { char: ".lg-title .ch:first-child" } }),
  part("LoginSub", ".lg-sub", start, { children: ".lg-sub", elements: { ".lg-sub": { set: { style: h("style") } } } }),
  field("LoginEmail", "email"),
  field("LoginField", "text"),
  part("LoginError", ".lg-err", emailError, {
    text: { message: ".lg-err" },
    elements: { ".lg-err": { set: { id: h("id") } } },
  }),
  part("LoginPrimary", ".lg-btn.primary", start, {
    slots: { label: ".lg-btn.primary" },
    elements: {
      ".lg-btn.primary": {
        set: { type: "submit", disabled: h("disabled"), style: h("style") },
      },
    },
  }),
  part("LoginOr", ".lg-or", start, { text: { label: ".lg-or > span" } }),
  part("LoginStack", ".lg-stack", start, { children: ".lg-stack" }),
  part("LoginButton", ".lg-stack > .lg-btn:first-child", start, {
    slots: { label: ".lg-stack > .lg-btn:first-child" },
    elements: {
      ".lg-stack > .lg-btn:first-child": {
        set: { onclick: h("onPress"), disabled: h("disabled"), "data-act": "", "data-p": "" },
      },
    },
  }),
  part("LoginConsent", ".lg-consent", consentError, {
    slots: { label: ".lg-consent > span:last-child" },
    elements: {
      ".lg-consent": { set: { class: `lg-consent ${h("consentCls")}` } },
      ".lg-consent input": {
        set: { checked: h("checked"), onclick: h("onToggle"), "aria-describedby": h("describedBy") },
      },
    },
    classes: { consentCls: ["bad"] },
  }),
  // A note under a step; a link in it goes to another page (link) or does something on this one (action).
  part("LoginNote", note, code, { children: note }),
  part("LoginLink", `${note} > a`, code, {
    slots: { label: `${note} > a` },
    elements: { [`${note} > a`]: { tag: "link", set: { href: h("to") } } },
  }),
  part("LoginAction", `${note} > a`, code, {
    slots: { label: `${note} > a` },
    elements: {
      [`${note} > a`]: { set: { href: "#", role: "button", onclick: h("onPress"), onkeydown: h("onKeyDown") } },
    },
  }),
] satisfies Region[];
