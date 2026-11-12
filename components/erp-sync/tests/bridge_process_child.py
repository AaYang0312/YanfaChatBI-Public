"""Test-only HTTP composition. No endpoint or mock switch exists in production.

Every request still passes through the pinned KuaimaiClient/sign/pagination and
real PostgreSQL transactions. This file never emits bridge protocol events.
"""
from __future__ import annotations

import argparse
from datetime import datetime
import json
import os
from pathlib import Path
import subprocess
import sys
from urllib.parse import parse_qs


def install_transport(scenario="success", audit=None, barrier=None):
    import httpx
    from bi_agent import kuaimai, sync

    original_client = httpx.Client
    original_connect = sync._connect
    paused = False

    def record(kind, **fields):
        if audit is not None:
            with Path(audit).open("a", encoding="utf-8") as stream:
                stream.write(json.dumps({"kind": kind, "pid": os.getpid(), **fields}) + "\n")

    def pause(kind):
        nonlocal paused
        if barrier is not None and barrier[0] == kind and not paused:
            paused = True
            record("barrier", stage=kind)
            os.write(barrier[1], b"R")
            if os.read(barrier[2], 1) != b"C":
                raise RuntimeError("synthetic barrier closed")

    def response(request):
        fields = {key: values[0] for key, values in parse_qs(request.content.decode()).items()}
        supplied = fields.pop("sign", "")
        if request.url != httpx.URL(kuaimai.ROUTER_URL) or supplied != kuaimai.sign(fields, "synthetic-secret"):
            raise AssertionError("actual request signature or fixed router differs")
        shop = fields["userIds"]
        method = fields["method"]
        record("http", shop=shop, method=method, cursor=fields.get("cursor"),
               page=fields.get("pageNo"), refetch="tid" in fields,
               start=fields.get("startTime", fields.get("startModified")),
               end=fields.get("endTime", fields.get("endModified")))
        pause("http")
        if "tid" in fields and scenario == "refetch-failure":
            return httpx.Response(403)
        if scenario == "page-failure" and shop == "166754" and fields.get("cursor"):
            return httpx.Response(403)
        moment = fields.get("endTime") or fields.get("endModified") or datetime.now().strftime("%Y-%m-%d %H:%M:%S")
        if method == sync.AFTERSALE_SOURCE:
            if scenario == "refetch-failure" and shop == "166754":
                rows = [{"id": "synthetic-refund", "userId": shop, "tid": "synthetic-unmatched",
                         "modified": moment, "onlineStatus": 0, "status": 1}]
                return httpx.Response(200, json={"list": rows, "total": 1})
            if scenario == "success" and shop == "166754":
                page = int(fields["pageNo"])
                indices = range(200) if page == 1 else range(200, 201)
                rows = [{"id": "synthetic-refund-" + str(index), "userId": shop,
                         "modified": moment, "onlineStatus": 0, "status": 1} for index in indices]
                return httpx.Response(200, json={"list": rows, "total": 201})
            return httpx.Response(200, json={"list": [], "total": 0})
        cursor = fields.get("cursor")
        row = {"sid": "synthetic-order-" + ("2" if cursor else "1"),
               "userId": shop, "updTime": moment, "orders": []}
        if "tid" in fields:
            row["tid"] = fields["tid"]
        return httpx.Response(200, json={"list": [row], "hasNext": not bool(cursor),
                                        "cursor": "synthetic-next" if not cursor else None})

    class ControlledClient(original_client):
        def __init__(self, *args, **kwargs):
            kwargs["transport"] = httpx.MockTransport(response)
            kwargs["trust_env"] = False
            super().__init__(*args, **kwargs)

        def close(self):
            super().close()
            record("http_closed")

        def __exit__(self, *args):
            result = super().__exit__(*args)
            record("http_closed")
            return result

    class Writer:
        def __init__(self, connection):
            self.connection = connection

        def __getattr__(self, name):
            return getattr(self.connection, name)

        def close(self):
            pause("writer-close")
            self.connection.close()
            record("writer_closed")

    def connect(dsn):
        record("connect")
        return Writer(original_connect(dsn))

    httpx.Client = ControlledClient
    sync._connect = connect


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scenario", choices=("success", "empty", "page-failure", "refetch-failure"), default="success")
    parser.add_argument("--audit")
    parser.add_argument("--barrier-stage", choices=("http", "writer-close"))
    parser.add_argument("--ready-fd", type=int)
    parser.add_argument("--continue-fd", type=int)
    parser.add_argument("--extra-lock-fd", type=int)
    parser.add_argument("--orphan-parent", action="store_true")
    args = parser.parse_args()
    if args.orphan_parent:
        child_args = [value for value in sys.argv[1:] if value != "--orphan-parent"]
        inherited = [3] + [fd for fd in (args.ready_fd, args.continue_fd, args.extra_lock_fd) if fd is not None]
        subprocess.Popen([sys.executable, "-B", "-u", __file__, *child_args],
                         pass_fds=tuple(set(inherited)), start_new_session=True)
        return 0
    if args.extra_lock_fd is not None and args.extra_lock_fd != 3:
        os.close(args.extra_lock_fd)
    barrier = (args.barrier_stage, args.ready_fd, args.continue_fd) if args.barrier_stage else None
    install_transport(args.scenario, args.audit, barrier)
    from bi_agent.yanfa_bridge import main as bridge_main
    return bridge_main(["incremental"])


if __name__ == "__main__":
    os._exit(main())
