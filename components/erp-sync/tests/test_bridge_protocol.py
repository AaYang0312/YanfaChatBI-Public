from __future__ import annotations

import copy
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
import threading
import unittest
from unittest.mock import patch

from bi_agent import yanfa_bridge as bridge

SHOPS = ["166754", "166879", "166889", "167156", "900511001", "900656971", "900698220"]
TRADE = "erp.trade.list.query"
OUTSTOCK = "erp.trade.outstock.simple.query"
AFTERSALE = "erp.aftersale.list.query"


def make_start(before=None):
    before = before or (datetime.now(timezone(timedelta(hours=8))) - timedelta(hours=1)).replace(microsecond=595293)
    mapping = {shop: OUTSTOCK if index % 2 else TRADE for index, shop in enumerate(SHOPS)}
    scope = {"shops": SHOPS, "sources": mapping, "aftersale": AFTERSALE}
    return {"protocol": "yanfa-erp-bridge/1", "type": "start",
            "job_id": "11111111-1111-4111-8111-111111111111",
            "source_id": "22222222-2222-4222-8222-222222222222",
            "operation_id": "33333333-3333-4333-8333-333333333333",
            "instance_id": "synthetic:44444444-4444-4444-8444-444444444444",
            "mode": "incremental", "lock_fd": 3, "component_ref": "synthetic-candidate",
            "component_digest": "a" * 64, "bridge_version": "yanfa-bridge/0.1.0",
            "config_ref": "synthetic-config", "config_revision": 1,
            "scope_digest": hashlib.sha256(json.dumps(scope, separators=(",", ":")).encode()).hexdigest(),
            "expected_shop_ids": list(SHOPS), "source_mapping": mapping, "aftersale_source": AFTERSALE,
            "expected_states": [{"source": mapping[shop] if entity == "orders" else AFTERSALE,
                                 "entity": entity, "shop_id": shop, "before_watermark": before.isoformat()}
                                for shop in SHOPS for entity in ("orders", "aftersales_occurrence")]}


def encode(frame):
    return json.dumps(frame, separators=(",", ":")).encode() + b"\n"


class StartProtocolTests(unittest.TestCase):
    def test_microseconds_survive_start_and_equivalent_timezone(self):
        frame = make_start()
        state = frame["expected_states"][0]
        moment = datetime.fromisoformat(state["before_watermark"])
        state["before_watermark"] = moment.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")
        parsed = bridge.decode_start(encode(frame))
        self.assertEqual(bridge._timestamp(parsed["expected_states"][0]["before_watermark"]), moment)
        self.assertEqual(moment.microsecond, 595293)

    def test_noncanonical_or_ambiguous_frames_are_rejected(self):
        frame = make_start()
        raw = encode(frame)
        invalid = [raw[:-1], raw + raw, raw + b"\n", raw.replace(b'"type":"start"', b'"type":"start","type":"start"'),
                   raw.replace(b'"source":', b'"SOURCE":', 1), b"\xff\n", b"[1]\n",
                   b"{" + b" " * bridge.LINE_LIMIT + b"}\n"]
        for key, value in (("protocol", "yanfa-source-sync/1"), ("mode", "backfill"),
                           ("lock_fd", True), ("lock_fd", 4), ("config_revision", 1.0),
                           ("config_revision", 0), ("job_id", "0" * 32),
                           ("component_digest", "A" * 64), ("bridge_version", "other/1")):
            changed = copy.deepcopy(frame)
            changed[key] = value
            invalid.append(encode(changed))
        changed = dict(frame, TYPE="start")
        invalid.append(encode(changed))
        for value in invalid:
            with self.subTest(value=value[:100]):
                with self.assertRaises(bridge.BridgeError):
                    bridge.decode_start(value)

    def test_scope_digest_and_every_unique_state_are_required(self):
        frame = make_start()
        mutations = []
        changed = copy.deepcopy(frame)
        changed["expected_shop_ids"][1] = changed["expected_shop_ids"][0]
        mutations.append(changed)
        changed = copy.deepcopy(frame)
        changed["expected_states"][1] = changed["expected_states"][0]
        mutations.append(changed)
        changed = copy.deepcopy(frame)
        changed["source_mapping"][SHOPS[0]] = OUTSTOCK
        mutations.append(changed)
        for bad in (None, "1970-01-01T00:00:00+08:00", "2026-11-07", "2026-11-07T00:00:00.1234567+08:00"):
            changed = copy.deepcopy(frame)
            changed["expected_states"][0]["before_watermark"] = bad
            mutations.append(changed)
        for changed in mutations:
            with self.subTest(frame=changed):
                with self.assertRaises(bridge.BridgeError):
                    bridge.decode_start(encode(changed))

    def test_raw_environment_duplicate_shops_cannot_be_collapsed(self):
        frame = make_start()
        values = {key: "synthetic" for key in bridge.ENV_KEYS}
        values["BI_SHOP_IDS"] = ",".join(SHOPS + [SHOPS[0]])
        with patch.dict(os.environ, values, clear=True):
            with self.assertRaises(bridge.BridgeError):
                bridge.load_settings(frame)
        values["BI_SHOP_IDS"] = ",".join(SHOPS[:-1] + [" " + SHOPS[-1]])
        with patch.dict(os.environ, values, clear=True):
            with self.assertRaises(bridge.BridgeError):
                bridge.load_settings(frame)

    def test_delayed_second_frame_is_rejected_before_execution(self):
        read_fd, write_fd = os.pipe()
        first_read = threading.Event()
        def sender():
            try:
                os.write(write_fd, encode(make_start()))
                first_read.wait(2)
                os.write(write_fd, encode(make_start()))
            finally:
                os.close(write_fd)
        original_read = os.read
        def observed_read(fd, count):
            data = original_read(fd, count)
            first_read.set()
            return data
        thread = threading.Thread(target=sender)
        thread.start()
        try:
            with patch.object(bridge.os, "read", observed_read):
                with self.assertRaises(bridge.BridgeError):
                    bridge.read_start(read_fd)
        finally:
            os.close(read_fd)
            thread.join(3)
        self.assertFalse(thread.is_alive())

    def test_digest_uses_go_field_order_sort_and_utf8(self):
        result = {"shop_id": "2", "status": "ok", "order_source": TRADE, "orders_count": 2,
                  "aftersales_occurrence_count": 3, "related_orders_refetch_completed": True,
                  "safe_error_code": ""}
        other = dict(result, shop_id="10", status="failed", safe_error_code="SHOP_FAILED",
                     related_orders_refetch_completed=False)
        expected = (b'[{"shop_id":"10","status":"failed","order_source":"erp.trade.list.query",'
                    b'"orders_count":2,"aftersales_occurrence_count":3,"related_orders_refetch_completed":false,'
                    b'"safe_error_code":"SHOP_FAILED"},{"shop_id":"2","status":"ok",'
                    b'"order_source":"erp.trade.list.query","orders_count":2,"aftersales_occurrence_count":3,'
                    b'"related_orders_refetch_completed":true,"safe_error_code":""}]')
        self.assertEqual(bridge.shop_digest([result, other]), hashlib.sha256(expected).hexdigest())
        self.assertEqual(bridge.compact({"ref": "汉<&\u2028"}), b'{"ref":"\xe6\xb1\x89\\u003c\\u0026\\u2028"}')


if __name__ == "__main__":
    unittest.main()
