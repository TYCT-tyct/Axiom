export default {
  description: "Your own sign-in: when this one ends, your two-step verification and your recovery codes.",
  account: { title: "Account", id: "Account ID" },
  session: {
    title: "This sign-in",
    detail: "If nothing is done, you are signed out at {{at}}.",
    signOut: "Sign out",
  },
  twoStep: {
    title: "Two-step verification",
    lede: "The admin asks for it every time you sign in, and it cannot be turned off.",
    verifier: "Authenticator",
    verifierDetail: "The 6-digit code it makes is asked for when you sign in.",
    bound: "Set up",
    unbound: "Not set up",
    codes: "Recovery codes",
    codesDetail:
      "For when your phone is not with you. Each works once; generating new ones makes the current ones stop working at once.",
    regenerate: "Generate new ones",
  },
};
