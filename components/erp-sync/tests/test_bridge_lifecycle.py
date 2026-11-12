from __future__ import annotations

from datetime import datetime, timedelta
import fcntl
import json
import os
from pathlib import Path
import select
import signal
import subprocess
import sys
import tempfile
import unittest

import psycopg
from psycopg.conninfo import conninfo_to_dict
from bi_agent import yanfa_bridge as bridge
from test_bridge_protocol import SHOPS, AFTERSALE, encode, make_start

HERE = Path(__file__).absolute().parent


def required_dsn(key, user):
    value = os.environ.get(key)
    if not value:
        raise RuntimeError(key + " required; isolated PG scenarios NOT RUN (never skipped PASS)")
    params = conninfo_to_dict(value)
    if (params.get("host") != "127.0.0.1" or params.get("port") != "18545" or
            params.get("dbname") != "bi_agent" or params.get("user") != user):
        raise RuntimeError("isolated DSN boundary differs: " + key)
    return value


def safe_env(writer):
    return {"BI_WRITER_DSN": writer, "BI_SHOP_IDS": ",".join(SHOPS),
            "KUAI_MAI_APP_KEY": "synthetic-key", "KUAI_MAI_APP_SECRET": "synthetic-secret",
            "KUAI_MAI_ACCESS_TOKEN": "synthetic-token", "KUAI_MAI_REFRESH_TOKEN": "synthetic-refresh",
            "PYTHONSAFEPATH": "1", "PYTHONNOUSERSITE": "1", "PYTHONDONTWRITEBYTECODE": "1",
            "LANG": "C.UTF-8", "TZ": "Asia/Shanghai"}


class BridgeLifecycleTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if os.environ.get("YANFA_SYNC_TEST_ALLOW_RESET") != "owned-18545-bi_agent":
            raise RuntimeError("explicit owned synthetic schema reset consent required")
        cls.admin_dsn = required_dsn("YANFA_SYNC_TEST_ADMIN_DSN", "sync_test_owner")
        cls.writer_dsn = required_dsn("YANFA_SYNC_TEST_WRITER_DSN", "v15_bridge_writer")
        cls.reader_dsn = required_dsn("YANFA_SYNC_TEST_READER_DSN", "v15_bridge_reader")
        cls.control_dsn = required_dsn("YANFA_SYNC_TEST_CONTROL_DSN", "v15_bridge_control")
        # Assert real target/identity too; DSN syntax alone is not proof.
        for dsn, user in ((cls.admin_dsn, "sync_test_owner"), (cls.writer_dsn, "v15_bridge_writer"),
                          (cls.reader_dsn, "v15_bridge_reader"), (cls.control_dsn, "v15_bridge_control")):
            with psycopg.connect(dsn, autocommit=True) as conn:
                actual = conn.execute("SELECT current_database(),current_user,inet_server_port()").fetchone()
                if actual != ("bi_agent", user, 18545):
                    raise RuntimeError("isolated server identity differs")

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="yanfa-bridge-test-")
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.frame = make_start()
        with psycopg.connect(self.admin_dsn, autocommit=True) as conn:
            conn.execute((HERE / "fixtures/sync_schema.sql").read_text())
            for index, shop in enumerate(SHOPS):
                conn.execute("INSERT INTO bi.shops(shop_id,platform) VALUES (%s,%s)",
                             (shop, "tb" if index % 2 else "fxg"))
            for state in self.frame["expected_states"]:
                conn.execute("INSERT INTO bi.sync_state(source,entity,shop_id,watermark) VALUES (%s,%s,%s,%s)",
                             (state["source"], state["entity"], state["shop_id"], state["before_watermark"]))
        self.children = []
        self.addCleanup(self.cleanup_children)

    def cleanup_children(self):
        for process in self.children:
            if process.poll() is None:
                process.kill()
                process.wait(timeout=10)
            for stream in (process.stdin, process.stdout, process.stderr):
                if stream is not None:
                    stream.close()

    def sql(self, query, params=(), dsn=None):
        with psycopg.connect(dsn or self.admin_dsn, autocommit=True) as conn:
            cursor = conn.execute(query, params)
            return cursor.fetchall() if cursor.description is not None else []

    def launch(self, frame=True, scenario="success", barrier=None, production=False, orphan=False):
        lock_path = self.directory / ("source-" + str(len(self.children)) + ".lock")
        lock = os.open(lock_path, os.O_CREAT | os.O_RDWR | os.O_EXCL, 0o600)
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        audit = self.directory / ("audit-" + str(len(self.children)) + ".jsonl")
        args = [sys.executable, "-B", "-u"]
        extra = []
        wait_ready = send_continue = None
        if production:
            args += ["-m", "bi_agent.yanfa_bridge", "incremental"]
        else:
            args += [str(HERE / "bridge_process_child.py"), "--scenario", scenario, "--audit", str(audit),
                     "--extra-lock-fd", str(lock)]
            if orphan:
                args += ["--orphan-parent"]
            if barrier:
                wait_ready, child_ready = os.pipe()
                child_continue, send_continue = os.pipe()
                extra = [child_ready, child_continue]
                args += ["--barrier-stage", barrier, "--ready-fd", str(child_ready),
                         "--continue-fd", str(child_continue)]
        keep = tuple(sorted({3, lock, *extra}))
        process = subprocess.Popen(args, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                   cwd=self.directory, env=safe_env(self.writer_dsn), pass_fds=keep,
                                   preexec_fn=lambda: os.dup2(lock, 3))
        self.children.append(process)
        for fd in extra:
            os.close(fd)
        if frame is not False:
            process.stdin.write(encode(self.frame) if frame is True else frame)
            process.stdin.close()
            process.stdin = None
        return process, lock, lock_path, audit, wait_ready, send_continue

    def finish(self, process, timeout=30):
        stdout, stderr = process.communicate(timeout=timeout)
        stdout = stdout or b""
        stderr = stderr or b""
        for secret in (b"synthetic-secret", b"synthetic-token", b"synthetic-refresh", self.writer_dsn.encode()):
            self.assertNotIn(secret, stdout + stderr)
        self.assertNotIn(b"Traceback", stderr)
        return [json.loads(line) for line in stdout.splitlines()], stderr

    def assert_locked(self, path):
        fd = os.open(path, os.O_RDONLY)
        try:
            with self.assertRaises(BlockingIOError):
                fcntl.flock(fd, fcntl.LOCK_SH | fcntl.LOCK_NB)
        finally:
            os.close(fd)

    def assert_released(self, path):
        fd = os.open(path, os.O_RDONLY)
        try:
            fcntl.flock(fd, fcntl.LOCK_SH | fcntl.LOCK_NB)
        finally:
            os.close(fd)

    def audit(self, path):
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def test_real_seven_shop_pagination_commits_and_one_cutoff(self):
        process, lock, path, audit, _, _ = self.launch()
        os.close(lock)
        events, stderr = self.finish(process)
        self.assertEqual(process.returncode, 0, stderr)
        self.assertEqual([event["seq"] for event in events], list(range(1, 10)))
        self.assertEqual([event["type"] for event in events], ["run_started"] + ["shop_finished"] * 7 + ["run_finished"])
        started, tail = events[0], events[-1]
        cutoff = datetime.fromisoformat(started["cutoff"])
        self.assertEqual(cutoff.microsecond, 0)
        self.assertEqual(cutoff.utcoffset(), timedelta(hours=8))
        self.assertEqual(started["expected_states"], self.frame["expected_states"])
        results = [{key: event[key] for key in ("shop_id", "status", "order_source", "orders_count",
                    "aftersales_occurrence_count", "related_orders_refetch_completed", "safe_error_code")}
                   for event in events[1:-1]]
        self.assertEqual([row["shop_id"] for row in results], SHOPS)
        self.assertTrue(all(row["status"] == "ok" and row["orders_count"] == 2 for row in results))
        self.assertEqual(results[0]["aftersales_occurrence_count"], 201)
        self.assertEqual(tail["shop_results_digest"], bridge.shop_digest(results))
        self.assertEqual((tail["processed_count"], tail["succeeded_count"], tail["failed_count"]), (7, 7, 0))
        rows = self.sql("SELECT watermark,last_error_code FROM bi.sync_state", dsn=self.reader_dsn)
        self.assertEqual(rows, [(cutoff, None)] * 14)
        self.assertEqual(self.sql("SELECT count(*) FROM bi.orders"), [(14,)])
        self.assertEqual(self.sql("SELECT count(*) FROM bi.aftersales"), [(201,)])
        calls = [row for row in self.audit(audit) if row["kind"] == "http"]
        self.assertEqual({row["end"] for row in calls}, {cutoff.strftime("%Y-%m-%d %H:%M:%S")})
        self.assertTrue(any(row["cursor"] == "synthetic-next" for row in calls))
        self.assertTrue(any(row["page"] == "2" for row in calls))
        self.assert_released(path)

    def test_refetch_failure_preserves_real_commits_and_reports_failed_shop(self):
        process, lock, path, audit, _, _ = self.launch(scenario="refetch-failure")
        os.close(lock)
        events, stderr = self.finish(process)
        self.assertNotEqual(process.returncode, 0)
        first = events[1]
        self.assertEqual((first["status"], first["orders_count"], first["aftersales_occurrence_count"]), ("failed", 2, 1))
        self.assertFalse(first["related_orders_refetch_completed"])
        self.assertEqual(first["safe_error_code"], "ERP_REJECTED")
        self.assertEqual((events[-1]["succeeded_count"], events[-1]["failed_count"]), (6, 1))
        self.assertEqual(self.sql("SELECT count(*) FROM bi.orders"), [(14,)])
        self.assertEqual(self.sql("SELECT count(*) FROM bi.aftersales WHERE NOT matched"), [(1,)])
        self.assertTrue(any(row.get("refetch") for row in self.audit(audit)))
        self.assert_released(path)

    def test_second_page_failure_rolls_back_that_window_only(self):
        process, lock, _, _, _, _ = self.launch(scenario="page-failure")
        os.close(lock)
        events, _ = self.finish(process)
        self.assertNotEqual(process.returncode, 0)
        self.assertEqual(events[1]["orders_count"], 0)
        self.assertEqual(self.sql("SELECT count(*) FROM bi.orders WHERE shop_id=%s", (SHOPS[0],)), [(0,)])
        self.assertEqual(self.sql("SELECT watermark,last_error_code FROM bi.sync_state WHERE shop_id=%s AND entity='orders'", (SHOPS[0],)),
                         [(datetime.fromisoformat(self.frame["expected_states"][0]["before_watermark"]), "authentication")])
        self.assertEqual(self.sql("SELECT count(*) FROM bi.orders WHERE shop_id<>%s", (SHOPS[0],)), [(12,)])

    def test_all_shop_preflight_rejects_before_any_http_or_commit(self):
        cases = ["microsecond-drift", "null", "missing", "future", "epoch", "source-drift", "unknown-platform"]
        for case in cases:
            with self.subTest(case=case):
                # Restore just the affected final key/platform; no valid earlier shop may write.
                state = self.frame["expected_states"][-1]
                self.sql("DELETE FROM bi.sync_state WHERE shop_id=%s AND entity=%s", (state["shop_id"], state["entity"]))
                self.sql("INSERT INTO bi.sync_state(source,entity,shop_id,watermark) VALUES (%s,%s,%s,%s)",
                         (state["source"], state["entity"], state["shop_id"], state["before_watermark"]))
                self.sql("UPDATE bi.shops SET platform='fxg' WHERE shop_id=%s", (SHOPS[-1],))
                if case == "missing":
                    self.sql("DELETE FROM bi.sync_state WHERE shop_id=%s AND entity=%s", (state["shop_id"], state["entity"]))
                elif case in ("source-drift", "unknown-platform"):
                    self.sql("UPDATE bi.shops SET platform=%s WHERE shop_id=%s", ("tb" if case == "source-drift" else "unknown", SHOPS[-1]))
                else:
                    before = datetime.fromisoformat(state["before_watermark"])
                    value = {"microsecond-drift": before + timedelta(microseconds=1), "null": None,
                             "future": datetime.now(bridge.BEIJING) + timedelta(days=1),
                             "epoch": datetime(1970, 1, 1, tzinfo=bridge.BEIJING)}[case]
                    self.sql("UPDATE bi.sync_state SET watermark=%s WHERE shop_id=%s AND entity=%s", (value, state["shop_id"], state["entity"]))
                process, lock, _, audit, _, _ = self.launch()
                os.close(lock)
                events, stderr = self.finish(process)
                self.assertNotEqual(process.returncode, 0)
                self.assertEqual(events, [])
                self.assertFalse(any(row["kind"] == "http" for row in self.audit(audit)))
                self.assertEqual(self.sql("SELECT count(*) FROM bi.sync_batches"), [(0,)])

    def test_start_eof_timeout_and_invalid_inputs_have_zero_activity(self):
        for raw in (b"", encode(self.frame) + encode(self.frame), b"\xff\n", False):
            with self.subTest(raw=raw):
                process, lock, _, audit, _, _ = self.launch(frame=raw, production=raw is False)
                os.close(lock)
                if raw is False:
                    # Leave stdin open through the real ten-second start budget.
                    process.wait(timeout=15)
                events, stderr = self.finish(process)
                self.assertNotEqual(process.returncode, 0)
                self.assertEqual(events, [])
                self.assertEqual(self.audit(audit), [])
                self.assertEqual(self.sql("SELECT count(*) FROM bi.sync_batches"), [(0,)])

    def test_matching_future_before_is_rejected_without_initialization(self):
        future = (datetime.now(bridge.BEIJING) + timedelta(days=1)).isoformat()
        self.frame["expected_states"][-1]["before_watermark"] = future
        self.sql("UPDATE bi.sync_state SET watermark=%s WHERE shop_id=%s AND entity='aftersales_occurrence'",
                 (future, SHOPS[-1]))
        process, lock, _, audit, _, _ = self.launch()
        os.close(lock)
        events, stderr = self.finish(process)
        self.assertEqual(events, [])
        self.assertEqual(stderr, b"WATERMARK_INVALID\n")
        self.assertNotEqual(process.returncode, 0)
        self.assertFalse(any(row["kind"] == "http" for row in self.audit(audit)))
        self.assertEqual(self.sql("SELECT count(*) FROM bi.sync_batches"), [(0,)])
        self.assertEqual(self.sql("SELECT watermark FROM bi.sync_state WHERE shop_id=%s AND entity='aftersales_occurrence'",
                                  (SHOPS[-1],)), [(datetime.fromisoformat(future),)])

    def test_incomplete_schema_fails_before_http_or_state_writes(self):
        self.sql("ALTER TABLE bi.order_items DROP COLUMN source_type")
        process, lock, _, audit, _, _ = self.launch()
        os.close(lock)
        events, _ = self.finish(process)
        self.assertEqual(events, [])
        self.assertNotEqual(process.returncode, 0)
        self.assertFalse(any(row["kind"] == "http" for row in self.audit(audit)))
        self.assertEqual(self.sql("SELECT count(*) FROM bi.sync_batches"), [(0,)])

    def test_pg_competitor_prevents_http_and_business_writes(self):
        with psycopg.connect(self.control_dsn, autocommit=True) as competitor:
            self.assertTrue(competitor.execute("SELECT pg_try_advisory_lock(7319041)").fetchone()[0])
            process, lock, _, audit, _, _ = self.launch()
            os.close(lock)
            events, stderr = self.finish(process)
            self.assertNotEqual(process.returncode, 0)
            self.assertEqual(stderr, b"WRITER_BUSY\n")
            self.assertEqual(events, [])
            self.assertFalse(any(row["kind"] == "http" for row in self.audit(audit)))
            self.assertEqual(self.sql("SELECT count(*) FROM bi.sync_batches"), [(0,)])

    def test_same_ofd_ex_survives_until_real_writer_close(self):
        process, lock, path, audit, ready, proceed = self.launch(barrier="writer-close")
        os.close(lock)
        try:
            self.assertTrue(select.select([ready], [], [], 30)[0])
            self.assertEqual(os.read(ready, 1), b"R")
            self.assert_locked(path)
            with psycopg.connect(self.control_dsn, autocommit=True) as control:
                self.assertFalse(control.execute("SELECT pg_try_advisory_lock(7319041)").fetchone()[0])
            records = self.audit(audit)
            self.assertTrue(any(row["kind"] == "http_closed" for row in records))
            self.assertFalse(any(row["kind"] == "writer_closed" for row in records))
            os.write(proceed, b"C")
            self.finish(process)
            self.assert_released(path)
            with psycopg.connect(self.control_dsn, autocommit=True) as control:
                self.assertTrue(control.execute("SELECT pg_try_advisory_lock(7319041)").fetchone()[0])
        finally:
            os.close(ready)
            os.close(proceed)

    def test_broken_output_aborts_without_fabricated_remaining_shops(self):
        process, lock, path, _, ready, proceed = self.launch(barrier="http")
        os.close(lock)
        try:
            self.assertTrue(select.select([ready], [], [], 15)[0])
            os.read(ready, 1)
            process.stdout.close()
            process.stdout = None
            os.write(proceed, b"C")
            _, stderr = self.finish(process)
            self.assertNotEqual(process.returncode, 0)
            self.assertEqual(stderr, b"RUNNER_FAILED\n")
            # Shop one can commit before its result write fails; no other shop ran.
            self.assertEqual(self.sql("SELECT DISTINCT shop_id FROM bi.sync_batches"), [(SHOPS[0],)])
            self.assert_released(path)
        finally:
            os.close(ready)
            os.close(proceed)

    def test_parent_process_death_keeps_child_ex_and_pg_until_child_exits(self):
        process, lock, path, audit, ready, proceed = self.launch(barrier="http", orphan=True)
        os.close(lock)
        child_pid = None
        try:
            process.wait(timeout=15)
            self.assertEqual(process.returncode, 0)
            self.assertTrue(select.select([ready], [], [], 15)[0])
            os.read(ready, 1)
            child_pid = next(row["pid"] for row in self.audit(audit) if row["kind"] == "barrier")
            self.assert_locked(path)
            with psycopg.connect(self.control_dsn, autocommit=True) as control:
                self.assertFalse(control.execute("SELECT pg_try_advisory_lock(7319041)").fetchone()[0])
            os.kill(child_pid, signal.SIGTERM)
            events, stderr = self.finish(process)
            self.assertEqual([row["type"] for row in events], ["run_started"])
            self.assertEqual(stderr, b"RUNNER_FAILED\n")
            self.assertEqual(self.sql("SELECT count(*) FROM bi.sync_batches"), [(0,)])
            self.assert_released(path)
            with psycopg.connect(self.control_dsn, autocommit=True) as control:
                self.assertTrue(control.execute("SELECT pg_try_advisory_lock(7319041)").fetchone()[0])
            child_pid = None
        finally:
            if child_pid is not None:
                try:
                    os.kill(child_pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            os.close(ready)
            os.close(proceed)


if __name__ == "__main__":
    unittest.main()
