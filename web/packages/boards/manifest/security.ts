import type { Region } from "../regions.ts";

const english = {
  mdfe0487d70: "Sign-in & security",
  mc70239e4f3: "How you sign in to Axiom.",
  ma621ab606d: "Password",
  m4a05923454: "Last changed 3 months ago",
  m08d0080624: "Change password",
  m0e219bca12: "Two-step verification",
  m2f363726b7: "In addition to your password, require a 6-digit code from your authenticator app",
  m6ba0f3509c: ". Required for all Axiom Labs members",
  md23b4e4fe5: "Signed-in devices",
  m949c8b537e: "Sign out other devices",
  m2adcd12cd5: "Shanghai, now",
  m602bdcadd9: "This device",
  m71e1947750: "Shanghai, 2 hours ago",
  m498e1d59b4: "Sign out",
  m2f85e0a1c9: "Hangzhou, 5 days ago",
};

/** @public loaded by regions.ts from the directory listing */
export default ["AxApp", "AxAppPhone"].map(
  (board): Region => ({
    board,
    name: "Security",
    roots: [".p-security"],
    page: "security",
    render: {
      root: ".p-security",
      actions: [["go", "security"]],
      viewport: { width: board === "AxAppPhone" ? 390 : 1440, height: 900 },
    },
    text: {
      sessionsTitle: "section.qcard:nth-of-type(2) .qch h2",
      sessionsClearLabel: "section.qcard:nth-of-type(2) .qch .btn span",
    },
    repeats: {
      authRows: "section.qcard:nth-of-type(1) > .qrow",
      sessions: ".qses",
    },
    elements: {
      ".p-security": { set: { style: "{{pageStyle}}" } },
      ".qph": { unless: "hideHead" },
      "section.qcard:nth-of-type(1)": { unless: "hideCard1" },
      "section.qcard:nth-of-type(1) .btn": { set: { onclick: "{{onAuthAction}}", disabled: "{{authActionDisabled}}" } },
      "section.qcard:nth-of-type(2)": { unless: "hideCard2" },
      "section.qcard:nth-of-type(2) .qch .btn": {
        unless: "hideRevokeOthers",
        set: { onclick: "{{onRevokeOthers}}", disabled: "{{revokeOthersDisabled}}" },
      },
      ".qses .btn": { set: { onclick: "{{onSessionRevoke}}", disabled: "{{sessionRevokeDisabled}}" } },
    },
    english,
    css: { ".ov .qses .btn.pri span": "color:inherit" },
  }),
);
