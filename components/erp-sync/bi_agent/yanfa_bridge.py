"""Pinned incremental adapter. Only main() owns inherited FD3 and process IO."""
from __future__ import annotations

import hashlib
import json
import logging
import os
import re
import select
import signal
import stat
import sys
import time
import uuid
from datetime import datetime
from zoneinfo import ZoneInfo

PROTOCOL = "yanfa-erp-bridge/1"
VERSION = "yanfa-bridge/0.1.0"
BEIJING = ZoneInfo("Asia/Shanghai")
START_SECONDS = 10.0
LINE_LIMIT = 16 * 1024
ENV_KEYS = (
    "BI_WRITER_DSN", "BI_SHOP_IDS", "KUAI_MAI_APP_KEY", "KUAI_MAI_APP_SECRET",
    "KUAI_MAI_ACCESS_TOKEN", "KUAI_MAI_REFRESH_TOKEN",
)
START_KEYS = frozenset((
    "protocol", "type", "job_id", "source_id", "operation_id", "instance_id",
    "mode", "lock_fd", "component_ref", "component_digest", "bridge_version",
    "config_ref", "config_revision", "scope_digest", "expected_shop_ids",
    "source_mapping", "aftersale_source", "expected_states",
))
STATE_KEYS = frozenset(("source", "entity", "shop_id", "before_watermark"))
TOKEN = re.compile(r"[A-Za-z0-9_.:/@+\-]{1,256}\Z")
DIGITS = re.compile(r"[0-9]{1,20}\Z")
SHA256 = re.compile(r"[0-9a-f]{64}\Z")
TIMESTAMP = re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,6})?(?:Z|[+-]\d{2}:\d{2})\Z")


class BridgeError(Exception):
    def __init__(self, code="RUNNER_FAILED"):
        self.code = code
        super().__init__(code)


class Terminated(BaseException):
    pass


def compact(value):
    # Go encoding/json escapes HTML and U+2028/U+2029 even in UTF-8 strings.
    text = json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)
    for char, escaped in (("&", "\\u0026"), ("<", "\\u003c"), (">", "\\u003e"),
                          ("\u2028", "\\u2028"), ("\u2029", "\\u2029")):
        text = text.replace(char, escaped)
    return text.encode("utf-8")


def scope_digest(shops, mapping, aftersale):
    return hashlib.sha256(compact({"shops": shops,
        "sources": {shop: mapping[shop] for shop in sorted(mapping)},
        "aftersale": aftersale})).hexdigest()


def shop_digest(results):
    return hashlib.sha256(compact(sorted(results, key=lambda row: row["shop_id"]))).hexdigest()


def _pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise BridgeError()
        result[key] = value
    return result


def _token(value, maximum=256):
    if not isinstance(value, str) or len(value) > maximum or not TOKEN.fullmatch(value):
        raise BridgeError()
    return value


def _timestamp(value):
    if not isinstance(value, str) or not TIMESTAMP.fullmatch(value):
        raise BridgeError("WATERMARK_INVALID")
    try:
        return datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        raise BridgeError("WATERMARK_INVALID") from None


def _invalid_constant(value):
    raise BridgeError()


def decode_start(raw):
    if not raw.endswith(b"\n") or len(raw) > LINE_LIMIT or b"\n" in raw[:-1]:
        raise BridgeError()
    try:
        frame = json.loads(raw.decode("utf-8", errors="strict"), object_pairs_hook=_pairs,
                           parse_constant=_invalid_constant)
    except (ValueError, UnicodeError, RecursionError):
        raise BridgeError() from None
    if not isinstance(frame, dict) or frame.keys() != START_KEYS:
        raise BridgeError()
    if (frame["protocol"] != PROTOCOL or frame["type"] != "start" or
            frame["mode"] != "incremental" or type(frame["lock_fd"]) is not int or
            frame["lock_fd"] != 3 or frame["bridge_version"] != VERSION):
        raise BridgeError()
    for key in ("job_id", "source_id", "operation_id"):
        value = frame[key]
        try:
            parsed = uuid.UUID(value) if isinstance(value, str) else None
        except ValueError:
            parsed = None
        if parsed is None or parsed.int == 0 or str(parsed) != value:
            raise BridgeError()
    for key in ("instance_id", "component_ref", "config_ref"):
        _token(frame[key])
    for key in ("component_digest", "scope_digest"):
        if not isinstance(frame[key], str) or not SHA256.fullmatch(frame[key]):
            raise BridgeError()
    revision = frame["config_revision"]
    if type(revision) is not int or not 1 <= revision <= 9223372036854775807:
        raise BridgeError()
    shops, mapping = frame["expected_shop_ids"], frame["source_mapping"]
    if (not isinstance(shops, list) or len(shops) != 7 or
            any(not isinstance(shop, str) or not DIGITS.fullmatch(shop) for shop in shops) or
            len(set(shops)) != 7 or shops != sorted(shops) or
            not isinstance(mapping, dict) or set(mapping) != set(shops)):
        raise BridgeError("SOURCE_MAPPING_MISMATCH")
    for source in mapping.values():
        _token(source, 128)
    _token(frame["aftersale_source"], 128)
    if scope_digest(shops, mapping, frame["aftersale_source"]) != frame["scope_digest"]:
        raise BridgeError("SOURCE_MAPPING_MISMATCH")
    states = frame["expected_states"]
    wanted = {(shop, entity, mapping[shop] if entity == "orders" else frame["aftersale_source"])
              for shop in shops for entity in ("orders", "aftersales_occurrence")}
    if not isinstance(states, list) or len(states) != 14:
        raise BridgeError("WATERMARK_INVALID")
    for state in states:
        if not isinstance(state, dict) or state.keys() != STATE_KEYS:
            raise BridgeError()
        if any(not isinstance(state[key], str) for key in STATE_KEYS):
            raise BridgeError()
        key = (state["shop_id"], state["entity"], state["source"])
        if key not in wanted:
            raise BridgeError("SOURCE_MAPPING_MISMATCH")
        wanted.remove(key)
        if _timestamp(state["before_watermark"]) <= datetime(1970, 1, 2, tzinfo=BEIJING):
            raise BridgeError("WATERMARK_INVALID")
    return frame


def read_start(fd=0):
    # EOF is required too: the parent writes exactly one line and closes stdin.
    # This rejects a delayed second frame before any database or ERP activity.
    deadline = time.monotonic() + START_SECONDS
    raw = bytearray()
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0 or not select.select([fd], [], [], remaining)[0]:
            raise BridgeError()
        part = os.read(fd, LINE_LIMIT + 1 - len(raw))
        if not part:
            return decode_start(bytes(raw))
        raw.extend(part)
        if len(raw) > LINE_LIMIT or (b"\n" in raw and raw.index(10) != len(raw) - 1):
            raise BridgeError()


def load_settings(frame):
    values = {key: os.environ.get(key, "") for key in ENV_KEYS}
    if any(not value or len(value) > 16384 or "\0" in value for value in values.values()):
        raise BridgeError()
    # Validate the raw list before the original loader converts it to frozenset.
    shops = values["BI_SHOP_IDS"].split(",")
    if len(shops) != 7 or len(set(shops)) != 7 or sorted(shops) != frame["expected_shop_ids"]:
        raise BridgeError("SOURCE_MAPPING_MISMATCH")
    from .config import load_sync_settings
    return load_sync_settings(values)


def preflight(conn, original, frame, cutoff):
    before = {(state["shop_id"], state["entity"], state["source"]):
              _timestamp(state["before_watermark"]) for state in frame["expected_states"]}
    if frame["aftersale_source"] != original.AFTERSALE_SOURCE:
        raise BridgeError("SOURCE_MAPPING_MISMATCH")
    with conn.transaction():
        conn.execute("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY")
        original.assert_sync_schema(conn)
        for shop in frame["expected_shop_ids"]:
            try:
                source = original._shop_order_source(conn, shop)
            except SystemExit:
                raise BridgeError("SOURCE_MAPPING_MISMATCH") from None
            if source != frame["source_mapping"][shop]:
                raise BridgeError("SOURCE_MAPPING_MISMATCH")
        for (shop, entity, source), expected in before.items():
            rows = conn.execute(
                "SELECT watermark FROM bi.sync_state WHERE source=%s AND entity=%s AND shop_id=%s",
                (source, entity, shop)).fetchall()
            if (len(rows) != 1 or not isinstance(rows[0][0], datetime) or
                    rows[0][0].tzinfo is None or rows[0][0] != expected or
                    expected <= datetime(1970, 1, 2, tzinfo=BEIJING) or expected > cutoff):
                raise BridgeError("WATERMARK_INVALID")


class Events:
    def __init__(self, frame):
        self.frame = frame
        self.seq = 0

    def emit(self, kind, **fields):
        self.seq += 1
        event = {"protocol": PROTOCOL, "job_id": self.frame["job_id"],
                 "source_id": self.frame["source_id"], "operation_id": self.frame["operation_id"],
                 "seq": self.seq, "type": kind, **fields}
        payload = compact(event) + b"\n"
        if len(payload) > LINE_LIMIT:
            raise BridgeError()
        # Flush failure propagates; no later shop or synthetic tail is emitted.
        sys.stdout.buffer.write(payload)
        sys.stdout.buffer.flush()


def _batches(conn, shop):
    return {str(batch): (entity, count) for batch, entity, count in conn.execute(
        "SELECT batch_id,entity,row_count FROM bi.sync_batches "
        "WHERE shop_id=%s AND mode='incremental' AND entity IN ('orders','aftersales_occurrence')",
        (shop,)).fetchall()}


def execute(frame):
    from . import sync as original
    import httpx

    settings = load_settings(frame)
    events = Events(frame)
    results = []
    conn = original._connect(settings.writer_dsn.get_secret_value())
    try:
        if not conn.execute("SELECT pg_try_advisory_lock(%s)", (original.LOCK_ID,)).fetchone()[0]:
            raise BridgeError("WRITER_BUSY")
        cutoff = datetime.now(BEIJING).replace(microsecond=0)
        preflight(conn, original, frame, cutoff)
        events.emit("run_started", bridge_version=VERSION, component_ref=frame["component_ref"],
                    mode="incremental", cutoff=cutoff.isoformat(), config_revision=frame["config_revision"],
                    scope_digest=frame["scope_digest"], expected_shop_ids=frame["expected_shop_ids"],
                    expected_states=frame["expected_states"])
        with httpx.Client(timeout=30.0) as http:
            client = original.KuaimaiClient(settings, http)
            for shop in frame["expected_shop_ids"]:
                baseline = _batches(conn, shop)
                code = ""
                try:
                    stats = original._incremental_shop(conn, client, shop_id=shop,
                        run_end=cutoff, order_source=frame["source_mapping"][shop])
                except (Exception, SystemExit) as exc:
                    code = "ERP_REJECTED" if isinstance(exc, original.KuaimaiError) else "SHOP_FAILED"
                    # An unusable writer fails here; do not invent unexecuted shops.
                    committed = _batches(conn, shop)
                    stats = {"orders": 0, "aftersales_occurrence": 0}
                    for batch, (entity, count) in committed.items():
                        if batch not in baseline:
                            stats[entity] += count
                result = {"shop_id": shop, "status": "failed" if code else "ok",
                          "order_source": frame["source_mapping"][shop],
                          "orders_count": stats["orders"],
                          "aftersales_occurrence_count": stats["aftersales_occurrence"],
                          "related_orders_refetch_completed": not bool(code), "safe_error_code": code}
                if any(type(result[key]) is not int or result[key] < 0 for key in
                       ("orders_count", "aftersales_occurrence_count")):
                    raise BridgeError()
                events.emit("shop_finished", **result)
                results.append(result)
        succeeded = sum(result["status"] == "ok" for result in results)
        events.emit("run_finished", cutoff=cutoff.isoformat(), expected_count=7,
                    processed_count=len(results), succeeded_count=succeeded,
                    failed_count=7 - succeeded, shop_results_digest=shop_digest(results))
        return 0 if succeeded == 7 else 1
    finally:
        # Session close releases the PG lock; never release FD3 before this.
        conn.close()


def _terminate(signum, frame):
    raise Terminated()


def main(argv=None):
    # Original log records may contain business identifiers. No original handler
    # or file logger is installed; protocol errors still return nonzero below.
    logging.disable(sys.maxsize)
    for signum in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(signum, _terminate)
    try:
        if (sys.argv[1:] if argv is None else argv) != ["incremental"]:
            raise BridgeError()
        inherited = os.fstat(3)
        if (not stat.S_ISREG(inherited.st_mode) or inherited.st_uid != os.geteuid() or
                stat.S_IMODE(inherited.st_mode) != 0o600 or inherited.st_nlink != 1):
            raise BridgeError()
        return execute(read_start())
    except (Exception, SystemExit, Terminated) as exc:
        code = exc.code if isinstance(exc, BridgeError) else "RUNNER_FAILED"
        try:
            os.write(2, (code + "\n").encode("ascii"))
        except OSError:
            pass
        return 1
    finally:
        try:
            os.close(3)
        except OSError:
            pass


if __name__ == "__main__":
    # Avoid interpreter shutdown retrying a broken stdout buffer with raw text.
    os._exit(main())
