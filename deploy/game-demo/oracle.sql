\set ON_ERROR_STOP on
-- Independent oracle: raw order lines, not the application source/metric engine.
SELECT o.shop_id,sum(l.quantity) quantity,sum(l.quantity*l.unit_price) sales,
 sum(l.quantity*l.unit_cost) cost,
 sum(l.quantity*(l.unit_price-l.unit_cost)) margin,
 sum(l.quantity*(l.unit_price-l.unit_cost))/NULLIF(sum(l.quantity*l.unit_price),0) margin_rate
FROM game_demo.orders o JOIN game_demo.order_lines l USING(order_id)
WHERE l.product_id='RE9-PS5-STD'
 AND o.paid_at >= '2026-11-02 00:00+08'::timestamptz
 AND o.paid_at < '2026-11-09 00:00+08'::timestamptz
GROUP BY o.shop_id ORDER BY o.shop_id;
DO $$ DECLARE actual numeric[]; BEGIN
 SELECT ARRAY[sum(l.quantity*l.unit_price),sum(l.quantity*l.unit_cost),sum(l.quantity*(l.unit_price-l.unit_cost))]
 INTO actual FROM game_demo.orders o JOIN game_demo.order_lines l USING(order_id)
 WHERE l.product_id='RE9-PS5-STD' AND o.paid_at >= '2026-11-02 00:00+08'::timestamptz AND o.paid_at < '2026-11-09 00:00+08'::timestamptz;
 IF actual<>ARRAY[12000,7200,4800]::numeric[] THEN RAISE EXCEPTION 'Main oracle failed: %',actual;END IF;
 IF NOT EXISTS (SELECT o.shop_id FROM game_demo.orders o JOIN game_demo.order_lines l USING(order_id)
  WHERE l.product_id='RE9-PS5-STD' AND o.shop_id='CAMPUS' AND o.paid_at >= '2026-11-02 00:00+08'::timestamptz AND o.paid_at < '2026-11-09 00:00+08'::timestamptz
  GROUP BY o.shop_id HAVING sum(l.quantity*l.unit_price)=6000 AND sum(l.quantity*l.unit_cost)=3000) THEN RAISE EXCEPTION 'Campus shop oracle failed'; END IF;
 IF NOT EXISTS (SELECT o.shop_id FROM game_demo.orders o JOIN game_demo.order_lines l USING(order_id)
  WHERE l.product_id='RE9-PS5-STD' AND o.shop_id='PIXEL' AND o.paid_at >= '2026-11-02 00:00+08'::timestamptz AND o.paid_at < '2026-11-09 00:00+08'::timestamptz
  GROUP BY o.shop_id HAVING sum(l.quantity*l.unit_price)=6000 AND sum(l.quantity*l.unit_cost)=4200) THEN RAISE EXCEPTION 'Pixel shop oracle failed'; END IF;
 IF NOT EXISTS(SELECT FROM reporting.v_game_daily WHERE shop_id='PIXEL' AND product_id='NR-PS5-STD' AND business_date='2026-11-05' AND coverage_status='missing' AND sales IS NULL) THEN RAISE EXCEPTION 'Missing became zero';END IF;
 IF NOT EXISTS(SELECT FROM reporting.v_game_daily WHERE product_id='NR-XBOX-STD' AND business_date='2026-11-08' AND coverage_status='covered' AND sales=0) THEN RAISE EXCEPTION 'Covered zero absent';END IF;
 IF (SELECT count(*) FROM game_demo.products WHERE title='ELDEN RING NIGHTREIGN')<>3 THEN RAISE EXCEPTION 'Ambiguity catalogue failed';END IF;
 RAISE NOTICE 'PASS: independent raw-order oracle, NULL missing day, covered zero, ambiguous catalogue';
END $$;
SELECT shop_id,business_date,coverage_status,quantity,sales FROM reporting.v_game_daily
WHERE product_id='NR-PS5-STD' AND shop_id='PIXEL' AND business_date BETWEEN '2026-11-04' AND '2026-11-06'
ORDER BY business_date;
