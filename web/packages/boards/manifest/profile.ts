import type { Region } from "../regions.ts";

const english = {
  mb94d13a45e: "Profile",
  m47cc03e27e: "Your personal details, shared across personal and team spaces.",
  m3ea2d23c90: "Avatar",
  m0d8f3231cc: "Lin Zhiyuan",
  m0cff3c9b1c: "L",
  m6773310492: "Name",
  m73075237fd: "Email",
  mfad68cf9c0: "Used for sign-in and notifications",
  m32b60cb802: "Delete account",
  m99cda4403d: "Your keys, logs, and settings will be deleted, and your balance refunded.",
  mf09d69b42a: "You are the owner of Axiom Labs. Transfer ownership before deleting.",
  ma48fa6108b: "Delete",
};

/** @public loaded by regions.ts from the directory listing */
export default ["AxApp", "AxAppPhone"].map(
  (board): Region => ({
    board,
    name: "Profile",
    roots: [".p-profile"],
    page: "profile",
    render: {
      root: ".p-profile",
      actions: [["go", "profile"]],
      viewport: { width: board === "AxAppPhone" ? 390 : 1440, height: 900 },
    },
    repeats: {
      rows: "section.qcard:not(.danger) > .qrow",
    },
    elements: {
      ".p-profile": { set: { style: "{{pageStyle}}" } },
      ".qph": { unless: "hideHead" },
      "section.qcard:not(.danger)": { unless: "hideMainCard" },
      ".qcard.danger": { unless: "hideDeletion" },
      ".qin": {
        set: {
          ref: "{{inputRef}}",
          oninput: "{{onInput}}",
          onkeydown: "{{onKeyDown}}",
          disabled: "{{inputDisabled}}",
        },
      },
    },
    english,
  }),
);
