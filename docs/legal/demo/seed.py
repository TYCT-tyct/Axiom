#!/usr/bin/env python3
"""Publish the demo legal texts to one development site through the admin API.

The script signs in, enrolls a TOTP factor when the account has none (the
admin API requires an MFA session to publish), and posts the terms and privacy
policy in Chinese and English as four locale versions with the normal publish
endpoint. It refuses anything but a loopback admin URL unless --allow-nonlocal
is passed, so a stray environment cannot publish the demo texts to a real site.

Required environment:
  AXIOM_ADMIN_URL   Admin origin, e.g. http://localhost:8082
  AXIOM_SITE_ID     Site ID from `axiom site init`
  AXIOM_EMAIL       Platform admin email
  AXIOM_PASSWORD    Platform admin password

Optional:
  AXIOM_TOTP_SECRET  Base32 TOTP secret, when the account already has MFA
  AXIOM_REASON       Audit reason, default: publish demo legal texts

Usage:
  python3 docs/legal/demo/seed.py --dry-run
  AXIOM_ADMIN_URL=http://localhost:18082 AXIOM_SITE_ID=sit_... \
  AXIOM_EMAIL=admin@example.test AXIOM_PASSWORD=... \
  python3 docs/legal/demo/seed.py
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import hmac
import json
import os
import struct
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from http.cookiejar import CookieJar
from pathlib import Path

HERE = Path(__file__).resolve().parent
DOCUMENTS = (
    ("terms", "zh", HERE / "terms.zh.txt"),
    ("terms", "en", HERE / "terms.en.txt"),
    ("privacy", "zh", HERE / "privacy.zh.txt"),
    ("privacy", "en", HERE / "privacy.en.txt"),
)
BANNERS = {
    "zh": "【示例文本（demo），不构成法律意见，正式上线前须由负责人和律师审定替换】",
    "en": "Demo text, not legal advice; replace before launch.",
}
MAX_BODY = 65536
LOOPBACK = {"localhost", "127.0.0.1", "::1"}


class SeedError(RuntimeError):
    pass


class Api:
    def __init__(self, base: str):
        self.base = base.rstrip("/")
        self.csrf = ""
        self.jar = CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(self.jar)
        )

    def _request(self, method: str, path: str, payload=None, headers=None):
        body = None if payload is None else json.dumps(payload).encode()
        request = urllib.request.Request(self.base + path, data=body, method=method)
        request.add_header("Accept", "application/json")
        request.add_header("Origin", self.base)
        if body is not None:
            request.add_header("Content-Type", "application/json")
        if self.csrf:
            request.add_header("X-CSRF-Token", self.csrf)
        for name, value in (headers or {}).items():
            request.add_header(name, value)
        try:
            with self.opener.open(request, timeout=30) as response:
                raw = response.read()
                return response.status, json.loads(raw) if raw else None
        except urllib.error.HTTPError as error:
            raw = error.read()
            try:
                detail = json.loads(raw)
            except ValueError:
                detail = {"body": raw.decode(errors="replace")}
            raise SeedError(f"{method} {path} failed: {error.code} {detail}") from None

    def get(self, path: str):
        return self._request("GET", path)[1]

    def post(self, path: str, payload=None, headers=None):
        return self._request("POST", path, payload if payload is not None else {}, headers)[1]


def totp(secret: str, at: float | None = None) -> str:
    padded = secret.strip().upper()
    padded += "=" * ((8 - len(padded) % 8) % 8)
    key = base64.b32decode(padded)
    counter = int((time.time() if at is None else at)) // 30
    digest = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
    offset = digest[-1] & 0x0F
    value = struct.unpack(">I", digest[offset : offset + 4])[0] & 0x7FFFFFFF
    return f"{value % 1_000_000:06d}"


def admin_path(site_id: str) -> str:
    return "/api/admin/v1/sites/" + urllib.parse.quote(site_id, safe="") + "/legal-documents"


def sign_in(api: Api, email: str, password: str) -> None:
    grant = api.post("/api/admin/v1/auth/sign-in", {"email": email, "password": password})
    api.csrf = grant["csrf_token"]
    if grant["status"] == "mfa_required":
        secret = os.environ.get("AXIOM_TOTP_SECRET", "").strip()
        if not secret:
            raise SeedError(
                "the account already has MFA; set AXIOM_TOTP_SECRET to a current TOTP secret"
            )
        verified = api.post("/api/admin/v1/auth/mfa", {"code": totp(secret)})
        api.csrf = verified["csrf_token"]
        return
    session = api.get("/api/admin/v1/session")
    if session["level"] == "mfa":
        return
    setup = api.post("/api/admin/v1/auth/totp/setup")
    print(
        f"enrolled TOTP; for later runs set AXIOM_TOTP_SECRET={setup['secret']}",
        file=sys.stderr,
    )
    confirmed = api.post(
        "/api/admin/v1/auth/totp/confirm",
        {
            "secret": setup["secret"],
            "password": password,
            "code": totp(setup["secret"]),
        },
    )
    api.csrf = confirmed["csrf_token"]
    if api.get("/api/admin/v1/session")["level"] != "mfa":
        raise SeedError("the session did not reach MFA level after enrolling TOTP")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dry-run", action="store_true", help="check the files without calling the API")
    parser.add_argument(
        "--allow-nonlocal",
        action="store_true",
        help="allow an admin URL that is not loopback; never use this for production",
    )
    args = parser.parse_args()

    for kind, locale, path in DOCUMENTS:
        body = path.read_text(encoding="utf-8")
        if not body.startswith(BANNERS[locale]):
            raise SeedError(f"{path} does not start with the {locale} demo banner")
        if len(body.encode()) > MAX_BODY:
            raise SeedError(f"{path} is larger than the {MAX_BODY}-byte API limit")
        print(f"{kind}/{locale}: {path.name}, {len(body)} characters, banner present")
    if args.dry_run:
        return 0

    base = os.environ.get("AXIOM_ADMIN_URL", "http://localhost:8082").rstrip("/")
    parsed = urllib.parse.urlparse(base)
    if parsed.scheme not in {"http", "https"} or not parsed.hostname:
        raise SeedError("AXIOM_ADMIN_URL must be an http(s) origin")
    if parsed.hostname not in LOOPBACK and not args.allow_nonlocal:
        raise SeedError(
            "refusing a non-loopback admin URL; pass --allow-nonlocal only for a known development site"
        )
    for name in ("AXIOM_SITE_ID", "AXIOM_EMAIL", "AXIOM_PASSWORD"):
        if not os.environ.get(name):
            raise SeedError(f"{name} is required")

    reason = os.environ.get("AXIOM_REASON", "publish demo legal texts")
    api = Api(base)
    sign_in(api, os.environ["AXIOM_EMAIL"], os.environ["AXIOM_PASSWORD"])
    print(f"signed in to {base}", file=sys.stderr)
    for kind, locale, path in DOCUMENTS:
        body = path.read_text(encoding="utf-8")
        published = api.post(
            admin_path(os.environ["AXIOM_SITE_ID"]),
            {"kind": kind, "locale": locale, "body": body, "reason": reason},
            {"Idempotency-Key": str(uuid.uuid4())},
        )
        print(f"{kind}/{locale}: published version {published['version']}")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except SeedError as error:
        print(f"seed: {error}", file=sys.stderr)
        sys.exit(1)
