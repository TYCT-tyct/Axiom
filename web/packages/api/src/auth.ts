import { adminApi, consoleApi, type Schemas, unwrap, unwrapEmpty } from "./index";

// The calls of the Session tag, which both surfaces offer under their own path with the same shapes (a session is valid
// only on the surface that issued it). Pages that sign a person in are written once against this and given one
// surface's calls. Typing the admin's calls as this interface is also the check that the two contracts still agree.
export type AuthApi = {
  session(signal: AbortSignal): Promise<Schemas["Session"]>;
  signIn(body: Schemas["SignInRequest"]): Promise<Schemas["SessionGrant"]>;
  verifyMfa(body: Schemas["MfaRequest"]): Promise<Schemas["SessionGrant"]>;
  signOut(): Promise<void>;
  beginTotpSetup(): Promise<Schemas["TotpSetup"]>;
  confirmTotpSetup(body: Schemas["TotpConfirmRequest"]): Promise<Schemas["FactorChange"]>;
  regenerateRecoveryCodes(body: Schemas["RecoveryCodesRequest"]): Promise<Schemas["FactorChange"]>;
  version(signal: AbortSignal): Promise<Schemas["Version"]>;
};

export const consoleAuth: AuthApi = {
  session: (signal) => unwrap(consoleApi.GET("/api/console/v1/session", { signal })),
  signIn: (body) => unwrap(consoleApi.POST("/api/console/v1/auth/sign-in", { body })),
  verifyMfa: (body) => unwrap(consoleApi.POST("/api/console/v1/auth/mfa", { body })),
  signOut: () => unwrapEmpty(consoleApi.POST("/api/console/v1/auth/sign-out")),
  beginTotpSetup: () => unwrap(consoleApi.POST("/api/console/v1/auth/totp/setup")),
  confirmTotpSetup: (body) => unwrap(consoleApi.POST("/api/console/v1/auth/totp/confirm", { body })),
  regenerateRecoveryCodes: (body) => unwrap(consoleApi.POST("/api/console/v1/auth/recovery-codes", { body })),
  version: (signal) => unwrap(consoleApi.GET("/api/console/v1/version", { signal })),
};

export const adminAuth: AuthApi = {
  session: (signal) => unwrap(adminApi.GET("/api/admin/v1/session", { signal })),
  signIn: (body) => unwrap(adminApi.POST("/api/admin/v1/auth/sign-in", { body })),
  verifyMfa: (body) => unwrap(adminApi.POST("/api/admin/v1/auth/mfa", { body })),
  signOut: () => unwrapEmpty(adminApi.POST("/api/admin/v1/auth/sign-out")),
  beginTotpSetup: () => unwrap(adminApi.POST("/api/admin/v1/auth/totp/setup")),
  confirmTotpSetup: (body) => unwrap(adminApi.POST("/api/admin/v1/auth/totp/confirm", { body })),
  regenerateRecoveryCodes: (body) => unwrap(adminApi.POST("/api/admin/v1/auth/recovery-codes", { body })),
  version: (signal) => unwrap(adminApi.GET("/api/admin/v1/version", { signal })),
};
