import { expect, it } from "vitest";
import { proofWorked, sha256, solveProof } from "./proof";

const hex = (bytes: Uint8Array) => [...bytes].map((byte) => byte.toString(16).padStart(2, "0")).join("");

it("hashes the standard vectors", () => {
  expect(hex(sha256(new TextEncoder().encode("")))).toBe(
    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  );
  expect(hex(sha256(new TextEncoder().encode("abc")))).toBe(
    "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
  );
});

it("finds a proof the server's rule accepts, bound to the site and address", async () => {
  const site = "site_01j00000000000000000000000";
  const nonce = await solveProof(site, "proof@example.com", 8);
  expect(proofWorked(site, "proof@example.com", nonce, 8)).toBe(true);
  expect(proofWorked(site, "other@example.com", nonce, 8)).toBe(false);
});
