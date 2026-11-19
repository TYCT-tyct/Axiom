import { knownEmail } from "./mock-auth.ts";

// The links the mocked server has mailed when a test starts, by token: the address each is for. A reset link works
// once for its person; a verification link once, while no account has its address (console-api.ts).

// Reset links: one for the mocked user, and one for another person.
export const goodResetToken = "a".repeat(43);
export const otherResetToken = "b".repeat(43);
export const otherEmail = "someone.else@example.com";

// Verification links of open registration: one for an address nobody has, and one for the mocked user's.
export const goodVerifyToken = "c".repeat(40);
export const takenVerifyToken = "d".repeat(40);
export const newEmail = "new.person@example.com";

export const mailedLinks = () => ({
  resetLinks: new Map([
    [goodResetToken, knownEmail],
    [otherResetToken, otherEmail],
  ]),
  verifyLinks: new Map([
    [goodVerifyToken, newEmail],
    [takenVerifyToken, knownEmail],
  ]),
});
