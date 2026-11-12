#!/usr/bin/env python3
"""Bootstrap the biagent-yanfachatbi stack: create app user, register the bi_agent
PostgreSQL connection, set the default LLM model, and connect (fetch schema).

Usage (after `docker compose -f docker-compose-biagent.yml --env-file .env.biagent -p biagent-yanfachatbi up -d`):
    python bootstrap_biagent.py

Reads nothing from disk; all values match .env.biagent defaults.
Idempotent: reuses the existing user/chat if present.
"""
import json
import sys
import time
import urllib.error
import urllib.request

BASE = "http://localhost:3000"
ADMIN_USER = "biagent-admin"
ADMIN_PASS = "biagent-admin-2026"
APP_USER = "biagent"
APP_EMAIL = "biagent@local.dev"
APP_PASS = "biagent-user-2026"
LLM_MODEL = "remote-magpie/cursor/grok-4.6"
CONNECTION = {
    "type": "postgresql",
    "host": "bi-agent-postgres",  # resolved over the shared bi-agent-pg_default network
    "port": "5432",
    "username": "postgres",
    "password": "",
    "database": "bi_agent",
    "ssl_mode": "disable",
}


def call(method, path, body=None, token=None):
    req = urllib.request.Request(BASE + path, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    data = json.dumps(body).encode() if body is not None else None
    try:
        with urllib.request.urlopen(req, data, timeout=60) as r:
            return r.status, json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode() or "{}")


def wait_backend():
    for _ in range(60):
        try:
            with urllib.request.urlopen(BASE + "/health", timeout=3) as r:
                if r.status == 200:
                    return
        except Exception:
            pass
        time.sleep(2)
    print("FATAL: backend not reachable at", BASE)
    sys.exit(1)


def main():
    wait_backend()

    # 1. signup secret from admin
    st, resp = call("POST", "/api/auth/generate-signup-secret",
                    {"username": ADMIN_USER, "password": ADMIN_PASS})
    if st not in (200, 201) or not resp.get("success"):
        print("FATAL: generate-signup-secret failed:", st, resp)
        sys.exit(1)
    secret = resp["data"]["secret"]

    # 2. signup app user (idempotent: skip if already exists)
    st, resp = call("POST", "/api/auth/signup", {
        "username": APP_USER, "email": APP_EMAIL,
        "password": APP_PASS, "user_signup_secret": secret,
    })
    if st not in (200, 201) and "exist" not in str(resp).lower():
        print("FATAL: signup failed:", st, resp)
        sys.exit(1)
    print("[ok] user ready:", APP_USER)

    # 3. login
    st, resp = call("POST", "/api/auth/login",
                    {"username_or_email": APP_USER, "password": APP_PASS})
    if st != 200 or not resp.get("success"):
        print("FATAL: login failed:", st, resp)
        sys.exit(1)
    token = resp["data"]["access_token"]

    # 4. find or create the bi_agent chat/connection
    st, resp = call("GET", "/api/chats?page=1&page_size=50", token=token)
    chats = (resp.get("data") or {}).get("chats") or []
    chat = next((c for c in chats
                 if (c.get("connection") or {}).get("database") == CONNECTION["database"]), None)
    if chat is None:
        st, resp = call("POST", "/api/chats",
                        {"connection": CONNECTION,
                         "settings": {"auto_execute_query": True,
                                      "share_data_with_ai": True,
                                      "non_tech_mode": False,
                                      "auto_generate_visualization": True}},
                        token=token)
        if st not in (200, 201) or not resp.get("success"):
            print("FATAL: create chat failed:", st, resp)
            sys.exit(1)
        chat = resp["data"]
        print("[ok] connection created:", chat["id"])
    else:
        print("[ok] connection exists:", chat["id"])
    chat_id = chat["id"]

    # 5. pin default LLM model for this chat
    call("PATCH", "/api/chats/" + chat_id,
         {"preferred_llm_model": LLM_MODEL}, token=token)

    # 6. connect: establishes the DB connection and fetches the schema
    st, resp = call("POST", "/api/chats/%s/connect" % chat_id,
                    {"stream_id": "bootstrap-000-0000"}, token=token)
    print("[connect]", st, json.dumps(resp, ensure_ascii=False)[:400])
    if st != 200 or not resp.get("success"):
        sys.exit(1)

    # 7. verify tables are visible
    st, resp = call("GET", "/api/chats/%s/tables" % chat_id, token=token)
    tables = resp.get("data", {}).get("tables") if isinstance(resp.get("data"), dict) else None
    if tables is not None:
        print("[ok] tables visible:", len(tables))
    print("DONE chat_id=%s" % chat_id)


if __name__ == "__main__":
    main()
