-- Synthetic schema only. Test harness requires explicit owned-cluster reset consent,
-- localhost:18545/bi_agent and sync_test_owner before executing this script.
DROP SCHEMA IF EXISTS bi CASCADE;
CREATE SCHEMA bi AUTHORIZATION sync_test_owner;
CREATE TABLE bi.shops (
    shop_id text PRIMARY KEY, platform text NOT NULL,
    display_name text, enabled boolean NOT NULL DEFAULT true,
    capabilities text[] NOT NULL DEFAULT '{}'
);
CREATE TABLE bi.sync_state (
    source text NOT NULL, entity text NOT NULL, shop_id text NOT NULL,
    watermark timestamptz, covered tstzmultirange NOT NULL DEFAULT '{}',
    last_success_at timestamptz, last_attempt_at timestamptz,
    last_error_code text, data_as_of timestamptz,
    quality_status text NOT NULL DEFAULT 'unknown', quality_rule text,
    PRIMARY KEY (source, entity, shop_id)
);
CREATE TABLE bi.sync_batches (
    source text NOT NULL, entity text NOT NULL, shop_id text NOT NULL,
    batch_id text NOT NULL, business_window tstzrange NOT NULL,
    window_kind text NOT NULL, mode text NOT NULL, row_count integer NOT NULL,
    PRIMARY KEY (source, entity, shop_id, batch_id)
);
CREATE TABLE bi.orders (
    shop_id text NOT NULL, erp_id text NOT NULL, commercial_ids text[] NOT NULL,
    split_parent_id text, source text NOT NULL, source_updated_at timestamptz NOT NULL,
    platform_modified_at timestamptz, paid_at timestamptz, consigned_at timestamptz,
    raw_pay_amount numeric, raw_payment numeric, raw_platform_payment numeric,
    raw_cost numeric, raw_gross_profit numeric, unified_status text, system_status text,
    active boolean NOT NULL, normalization_status text NOT NULL, batch_id text,
    PRIMARY KEY (shop_id, erp_id)
);
CREATE TABLE bi.order_items (
    shop_id text NOT NULL, erp_id text NOT NULL, line_id text NOT NULL,
    commercial_id text, platform_line_id text, product_id text, source_type text,
    sku_id text, paid_at timestamptz, quantity numeric, gift_quantity numeric,
    raw_paid_amount numeric, raw_payment numeric, raw_unit_cost numeric,
    allocated_paid_amount numeric, allocation_verified boolean NOT NULL DEFAULT false,
    line_kind text, active boolean NOT NULL DEFAULT true,
    product_name_snapshot text, sku_label_snapshot text,
    PRIMARY KEY (shop_id, erp_id, line_id)
);
CREATE TABLE bi.order_payments (
    shop_id text NOT NULL, commercial_id text NOT NULL, paid_at timestamptz,
    amount numeric, currency text, basis text, verified boolean NOT NULL,
    source_updated_at timestamptz, PRIMARY KEY (shop_id, commercial_id)
);
CREATE TABLE bi.aftersales (
    shop_id text NOT NULL, aftersale_id text NOT NULL, platform_refund_id text,
    commercial_id text, erp_id text, raw_platform_amount numeric,
    raw_system_amount numeric, online_status integer, work_status integer,
    platform_completed_at timestamptz, system_completed_at timestamptz,
    source_updated_at timestamptz NOT NULL, platform_success boolean NOT NULL,
    matched boolean NOT NULL DEFAULT false, refund_canonical boolean NOT NULL DEFAULT false,
    batch_id text, PRIMARY KEY (shop_id, aftersale_id)
);
CREATE TABLE bi.products (product_id text PRIMARY KEY, title text, source_modified_at timestamptz);
GRANT USAGE ON SCHEMA bi TO v15_bridge_writer, v15_bridge_reader, v15_bridge_control;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA bi TO v15_bridge_writer;
GRANT SELECT ON ALL TABLES IN SCHEMA bi TO v15_bridge_reader, v15_bridge_control;
