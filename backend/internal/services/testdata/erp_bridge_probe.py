"""Test-only yanfa protocol/process probe. Never an ERP sync implementation.

Production has no code path to this file. The tests alone choose its executable,
argv, synthetic events and faults; it cannot attest ERP increment correctness.
"""
import datetime
import hashlib
import json
import os
import select
import signal
import sys
import time


def emit(value):
    sys.stdout.write(json.dumps(value, ensure_ascii=False, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def main():
    if os.environ.get("YANFA_TEST_ONLY_PROBE") != "1":
        return 78
    mode = os.environ.get("YANFA_PROBE_CASE", "ok")
    lock = os.fstat(3)
    if not select.select([sys.stdin], [], [], 10)[0]:
        return 79
    line = sys.stdin.buffer.readline(16385)
    if not line or len(line) > 16384:
        return 79
    frame = json.loads(line)
    if frame["protocol"] != "yanfa-erp-bridge/1" or frame["type"] != "start":
        return 79
    record = os.environ.get("YANFA_PROBE_RECORD")
    if record:
        with open(record, "a", encoding="utf-8") as output:
            output.write(json.dumps({"pid": os.getpid(), "pgid": os.getpgrp(),
                "device": lock.st_dev, "inode": lock.st_ino,
                "job_id": frame["job_id"], "operation_id": frame["operation_id"]}) + "\n")
    if mode == "hold":
        while True:
            time.sleep(1)
    if mode == "output_limit":
        sys.stderr.write("synthetic-private-canary-" * 60000)
        sys.stderr.flush()
        return 0
    if mode == "line_limit":
        sys.stdout.write("x" * 16385 + "\n")
        sys.stdout.flush()
        return 0
    if mode == "fork_descendant":
        child = os.fork()
        if child == 0:
            while True:
                time.sleep(1)
        return 0
    if mode == "escaped_fd":
        child = os.fork()
        if child == 0:
            os.setsid()
            os.close(1)
            os.close(2)
            if record:
                with open(record + ".escaped", "w", encoding="utf-8") as output:
                    output.write(str(os.getpid()))
            while True:
                time.sleep(1)
        return 0
    if mode == "partial_stdout_eof":
        os.write(1, b'{"incomplete":')
        return 0
    if mode == "stderr_total_limit":
        # Cross the combined byte budget without crossing any line budget.
        sys.stderr.write(("x" * 1023 + "\n") * 1025)
        sys.stderr.flush()
        return 0
    common = {"protocol": "yanfa-erp-bridge/1", "job_id": frame["job_id"],
        "source_id": frame["source_id"], "operation_id": frame["operation_id"]}
    if mode in ("abort_partial_stdout", "abort_partial_stderr"):
        def terminated(_signal, _frame):
            if record:
                with open(record + ".terminated", "w", encoding="utf-8") as output:
                    json.dump({"pid": os.getpid(), "signal": "TERM"}, output)
            os._exit(0)

        signal.signal(signal.SIGTERM, terminated)
        invalid = json.dumps(dict(common, seq=1, type="invalid_event"), separators=(",", ":")).encode()
        if mode == "abort_partial_stderr":
            os.write(2, b"synthetic partial stderr")
            os.write(1, invalid + b"\n")
        else:
            # One atomic pipe write puts the bad frame and partial tail into
            # the same read path before the parent can stop the writer.
            os.write(1, invalid + b"\n" + b'{"partial":')
        while True:
            signal.pause()
    if mode == "wrong_protocol":
        common["protocol"] = "yanfa-erp-bridge/2"
    if mode == "wrong_source":
        common["source_id"] = "00000000-0000-4000-8000-000000000001"
    seq = 0
    if mode == "out_of_order":
        seq = 1

    def event(kind, fields):
        nonlocal seq
        seq += 1
        value = dict(common, seq=seq, type=kind)
        value.update(fields)
        if mode == "unknown_field" and seq == 1:
            value["unexpected"] = True
        if mode == "case_alias_required_only" and kind == "run_started":
            value["BRIDGE_VERSION"] = value.pop("bridge_version")
        if mode == "case_alias_nested_state" and kind == "run_started":
            value["expected_states"] = [dict(state) for state in value["expected_states"]]
            nested = value["expected_states"][0]
            original_before = nested["before_watermark"]
            nested["before_watermark"] = "2099-01-01T00:00:00+08:00"
            nested["BEFORE_WATERMARK"] = original_before
        if mode == "case_alias_count_conflict" and kind == "shop_finished" and seq == 2:
            value["orders_count"] = 1
            value["ORDERS_COUNT"] = 2
        emit(value)
        return value

    cutoff = datetime.datetime.now(datetime.timezone(datetime.timedelta(hours=8))).replace(microsecond=0).isoformat()
    if mode == "future_cutoff":
        cutoff = (datetime.datetime.now(datetime.timezone(datetime.timedelta(hours=8))) + datetime.timedelta(days=1)).replace(microsecond=0).isoformat()
    states = frame["expected_states"]
    shops = sorted({state["shop_id"] for state in states})
    order_sources = {state["shop_id"]: state["source"] for state in states if state["entity"] == "orders"}
    started = event("run_started", {"bridge_version": "test-probe/1", "component_ref": "test-only-probe",
        "mode": "incremental", "cutoff": cutoff, "config_revision": frame["config_revision"],
        "scope_digest": frame["scope_digest"], "expected_shop_ids": shops, "expected_states": states})
    if mode == "duplicate":
        emit(started)
    if mode == "conflicting_seq":
        started["scope_digest"] = "0" * 64
        emit(started)
        return 0
    results = []
    for index, shop in enumerate(shops):
        if mode == "missing_shop" and index == 6:
            continue
        result = {"shop_id": shop, "status": "ok", "order_source": order_sources[shop],
            "orders_count": 0, "aftersales_occurrence_count": 0,
            "related_orders_refetch_completed": True, "safe_error_code": ""}
        if mode in ("case_alias_count_conflict", "case_alias_seq_conflict") and index == 0:
            result["orders_count"] = 2
        if mode == "shop_error_exit0" and index == 0:
            result.update(status="failed", related_orders_refetch_completed=False, safe_error_code="SHOP_FAILED")
        if mode == "null_required" and index == 0:
            result["safe_error_code"] = None
        emitted = event("shop_finished", result)
        results.append(result)
        if mode == "case_alias_seq_conflict" and index == 0:
            conflicting = dict(emitted, orders_count=1)
            conflicting["ORDERS_COUNT"] = 2
            emit(conflicting)
        if mode == "duplicate_shop" and index == 0:
            event("shop_finished", result)
        if mode == "conflicting_shop" and index == 0:
            event("shop_finished", dict(result, orders_count=1))
            return 0
    if mode == "missing_tail":
        return 0
    digest = hashlib.sha256(json.dumps(results, ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()
    ok = sum(result["status"] == "ok" for result in results)
    event("run_finished", {"cutoff": cutoff, "expected_count": 7, "processed_count": 7,
        "succeeded_count": ok, "failed_count": 7 - ok, "shop_results_digest": digest})
    if mode == "after_tail":
        event("shop_finished", results[0])
    return 0


if __name__ == "__main__":
    sys.exit(main())
