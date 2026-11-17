\set ON_ERROR_STOP on
-- Entire business dataset is SYNTHETIC. Only game titles/platforms/editions
-- refer to real products; see SOURCES.md. No real prices/orders/people.
BEGIN;
DO $$ BEGIN
 IF current_database() <> 'gameshelf_demo' THEN
  RAISE EXCEPTION 'Refusing to seed any database other than gameshelf_demo';
 END IF;
END $$;
CREATE SCHEMA IF NOT EXISTS game_demo;
CREATE SCHEMA IF NOT EXISTS reporting;
DO $$ BEGIN
 IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='game_demo_reader') THEN
  CREATE ROLE game_demo_reader LOGIN PASSWORD 'synthetic-demo-reader-only';
 END IF;
END $$;
ALTER ROLE game_demo_reader SET default_transaction_read_only=on;
ALTER ROLE game_demo_reader SET statement_timeout='8s';
ALTER ROLE game_demo_reader SET search_path=reporting,game_demo,public;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE TABLE IF NOT EXISTS game_demo.metadata (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 contract_version text NOT NULL, data_as_of timestamptz NOT NULL,
 data_notice text NOT NULL
);
CREATE TABLE IF NOT EXISTS game_demo.shops (
 shop_id text PRIMARY KEY, shop_name text NOT NULL, channel text NOT NULL DEFAULT 'DEMO-WEB'
);
CREATE TABLE IF NOT EXISTS game_demo.products (
 product_id text PRIMARY KEY, sku_id text UNIQUE NOT NULL, title text NOT NULL,
 gaming_platform text NOT NULL, edition text NOT NULL,
 format text NOT NULL CHECK (format='physical'), source_url text NOT NULL
);
CREATE TABLE IF NOT EXISTS game_demo.orders (
 order_id text PRIMARY KEY, shop_id text NOT NULL REFERENCES game_demo.shops,
 paid_at timestamptz NOT NULL, status text NOT NULL CHECK (status='paid')
);
CREATE TABLE IF NOT EXISTS game_demo.order_lines (
 order_id text NOT NULL REFERENCES game_demo.orders, line_no integer NOT NULL,
 product_id text NOT NULL REFERENCES game_demo.products,
 quantity integer NOT NULL CHECK (quantity>0),
 unit_price numeric(12,2) NOT NULL CHECK (unit_price>=0),
 unit_cost numeric(12,2) NOT NULL CHECK (unit_cost>=0),
 currency text NOT NULL CHECK (currency='HKD'), PRIMARY KEY(order_id,line_no)
);
CREATE TABLE IF NOT EXISTS game_demo.coverage (
 shop_id text REFERENCES game_demo.shops, product_id text REFERENCES game_demo.products,
 business_date date NOT NULL, status text NOT NULL CHECK(status IN ('covered','missing')),
 reason text NOT NULL, PRIMARY KEY(shop_id,product_id,business_date)
);
CREATE TABLE IF NOT EXISTS game_demo.inventory (
 shop_id text REFERENCES game_demo.shops, product_id text REFERENCES game_demo.products,
 snapshot_date date NOT NULL, units_on_hand integer NOT NULL CHECK(units_on_hand>=0),
 PRIMARY KEY(shop_id,product_id,snapshot_date)
);
-- Idempotent, transactional reset of this schema's synthetic rows only.
TRUNCATE game_demo.order_lines,game_demo.orders,game_demo.coverage,game_demo.inventory,game_demo.products,game_demo.shops,game_demo.metadata;
INSERT INTO game_demo.metadata VALUES (true,'game-shop-demo-v1','2026-11-09 00:00:00+08','SYNTHETIC coursework demo: every business number is invented; not market data');
INSERT INTO game_demo.shops VALUES ('CAMPUS','Campus Quest 校园游戏店','DEMO-WEB'),('PIXEL','Pixel Union 像素玩家店','DEMO-WEB');
INSERT INTO game_demo.products VALUES
 ('RE9-PS5-STD','SKU-RE9-PS5-STD','Resident Evil Requiem','PlayStation 5','Standard','physical','https://www.target.com/p/-/A-95046814'),
 ('NR-PS5-STD','SKU-NR-PS5-STD','ELDEN RING NIGHTREIGN','PlayStation 5','Standard','physical','https://store.bandainamcoent.com/games/elden-ring/nightreign/standard-edition'),
 ('NR-XBOX-STD','SKU-NR-XBOX-STD','ELDEN RING NIGHTREIGN','Xbox Series X','Standard','physical','https://store.bandainamcoent.com/games/elden-ring'),
 ('NR-PS5-COLLECTOR','SKU-NR-PS5-COLLECTOR','ELDEN RING NIGHTREIGN','PlayStation 5','Collector','physical','https://store.bandainamcoent.eu/elden-ring-nightreign-physical-full-game-ps5-collectors-edition-eu/');
INSERT INTO game_demo.coverage
 SELECT s.shop_id,p.product_id,d::date,'covered','synthetic import complete'
 FROM game_demo.shops s CROSS JOIN game_demo.products p
 CROSS JOIN generate_series('2026-10-26'::date,'2026-11-08'::date,interval '1 day') d;
UPDATE game_demo.coverage SET status='missing',reason='simulated missing daily import; sales unknown'
 WHERE shop_id='PIXEL' AND product_id='NR-PS5-STD' AND business_date='2026-11-05';
-- Main case: 20 units * HKD300 per shop; only cost differs (150 vs 210).
-- Prior week is complete, to support independently checkable comparisons.
INSERT INTO game_demo.orders
 SELECT 'DEMO-RE9-'||s.shop_id||'-'||d::date,s.shop_id,d::date+time '12:00' AT TIME ZONE 'Asia/Shanghai','paid'
 FROM game_demo.shops s CROSS JOIN (VALUES ('2026-10-27'::date),('2026-10-30'::date),('2026-11-03'::date),('2026-11-06'::date)) v(d);
INSERT INTO game_demo.order_lines
 SELECT o.order_id,1,'RE9-PS5-STD',CASE WHEN (o.paid_at AT TIME ZONE 'Asia/Shanghai')::date<'2026-11-02' THEN 8 ELSE 10 END,
 300,CASE WHEN o.shop_id='CAMPUS' THEN 150 ELSE 210 END,'HKD'
 FROM game_demo.orders o;
-- Other games: synthetic daily orders. Missing import creates no fake sales.
INSERT INTO game_demo.orders
 SELECT 'DEMO-'||s.shop_id||'-'||p.product_id||'-'||d::date,s.shop_id,d::date+time '17:00' AT TIME ZONE 'Asia/Shanghai','paid'
 FROM game_demo.shops s CROSS JOIN game_demo.products p
 CROSS JOIN generate_series('2026-10-26'::date,'2026-11-07'::date,interval '1 day') d
 WHERE p.product_id<>'RE9-PS5-STD'
 AND NOT(s.shop_id='PIXEL' AND p.product_id='NR-PS5-STD' AND d::date='2026-11-05');
INSERT INTO game_demo.order_lines
 SELECT o.order_id,1,p.product_id,
 CASE WHEN p.product_id='NR-PS5-STD' THEN 2 ELSE 1 END,
 CASE WHEN p.product_id='NR-PS5-COLLECTOR' THEN 800 ELSE 200 END,
 CASE WHEN p.product_id='NR-PS5-COLLECTOR' THEN 560 ELSE 120 END,'HKD'
 FROM game_demo.orders o JOIN game_demo.products p ON strpos(o.order_id,'-'||p.product_id||'-')>0
 WHERE p.product_id<>'RE9-PS5-STD';
INSERT INTO game_demo.inventory
 SELECT s.shop_id,p.product_id,'2026-11-08',CASE WHEN s.shop_id='CAMPUS' THEN 18 ELSE 6 END
 FROM game_demo.shops s CROSS JOIN game_demo.products p;

CREATE OR REPLACE VIEW reporting.v_game_sales_daily AS
 SELECT o.shop_id,l.product_id,(o.paid_at AT TIME ZONE 'Asia/Shanghai')::date business_date,
 sum(l.quantity)::numeric quantity,sum(l.quantity*l.unit_price) sales,sum(l.quantity*l.unit_cost) cost,'HKD'::text currency
 FROM game_demo.orders o JOIN game_demo.order_lines l USING(order_id)
 JOIN game_demo.coverage c ON c.shop_id=o.shop_id AND c.product_id=l.product_id
 AND c.business_date=(o.paid_at AT TIME ZONE 'Asia/Shanghai')::date AND c.status='covered'
 GROUP BY o.shop_id,l.product_id,(o.paid_at AT TIME ZONE 'Asia/Shanghai')::date;
-- Use this view for NL->SQL trends. Missing values stay NULL; covered no-sale
-- cells become 0. It must not be replaced by COALESCE on all missing values.
CREATE OR REPLACE VIEW reporting.v_game_daily AS
 SELECT c.shop_id,s.shop_name,c.product_id,p.sku_id,p.title,p.gaming_platform,p.edition,
 c.business_date,c.status coverage_status,c.reason coverage_reason,'HKD'::text currency,
 CASE WHEN c.status='covered' THEN COALESCE(f.quantity,0) END quantity,
 CASE WHEN c.status='covered' THEN COALESCE(f.sales,0) END sales,
 CASE WHEN c.status='covered' THEN COALESCE(f.cost,0) END cost,
 CASE WHEN c.status='covered' THEN COALESCE(f.sales,0)-COALESCE(f.cost,0) END margin
 FROM game_demo.coverage c JOIN game_demo.shops s USING(shop_id)
 JOIN game_demo.products p USING(product_id)
 LEFT JOIN reporting.v_game_sales_daily f USING(shop_id,product_id,business_date);
COMMENT ON VIEW reporting.v_game_daily IS 'SYNTHETIC game-shop data. Payment basis, Asia/Shanghai, HKD. Missing coverage -> NULL, covered zero sales -> 0. Never infer causality from these sales alone.';
COMMENT ON COLUMN reporting.v_game_daily.gaming_platform IS 'Console platform (PS5 / Xbox), distinct from sales channel DEMO-WEB.';
COMMENT ON COLUMN reporting.v_game_daily.margin IS 'Synthetic gross profit = sales - merchandise acquisition cost; before shipping, fees, tax and refunds. Not net profit.';
GRANT CONNECT ON DATABASE gameshelf_demo TO game_demo_reader;
GRANT USAGE ON SCHEMA game_demo,reporting TO game_demo_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA game_demo,reporting TO game_demo_reader;
COMMIT;
