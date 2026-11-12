-- Owner-run deployment step for the Assistant ERP quantity pilot.
-- Run against the approved ERP reporting database after verifying the source
-- columns and eligibility below. Application startup and sync never apply this file.
--
-- Use the reporting view owner with SELECT access to bi.order_items and bi.products.
-- After reviewing the view and its ACL, grant USAGE on reporting (if needed) and
-- SELECT on reporting.v_assistant_ordinary_sales only to the deployment's approved
-- Assistant read-only role. Keep its existing v_shops and v_coverage access.
-- Do not grant that role direct access to bi tables for this projection.
-- Check owner default privileges as well as explicit grants before enabling the pilot.

BEGIN;

CREATE VIEW reporting.v_assistant_ordinary_sales AS
SELECT
    i.shop_id,
    (i.paid_at AT TIME ZONE 'Asia/Shanghai')::date AS day,
    i.product_id,
    i.quantity,
    p.title AS product_name,
    i.product_name_snapshot,
    i.sku_label_snapshot AS sku_label
FROM bi.order_items AS i
LEFT JOIN bi.products AS p ON p.product_id = i.product_id
WHERE i.source_type = '0'
  -- The sync normalizer can also emit 'sale' for an unrecognized source type.
  AND i.line_kind = 'sale'
  AND i.active
  AND i.paid_at IS NOT NULL
  AND NULLIF(btrim(i.product_id), '') IS NOT NULL
  -- Match allocated-paid eligibility, including a zero allocated amount.
  AND i.allocated_paid_amount IS NOT NULL;

REVOKE ALL ON reporting.v_assistant_ordinary_sales FROM PUBLIC;

COMMIT;
