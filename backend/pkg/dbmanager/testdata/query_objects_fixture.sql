-- Synthetic fixture only. Execute in a fresh isolated query_objects_test DB.
-- Do not run against ERP/bi_agent or an existing database.
CREATE ROLE query_objects_reader NOLOGIN;
CREATE SCHEMA private_base;
CREATE SCHEMA reporting;
CREATE SCHEMA "Reports.X";
CREATE TABLE private_base.sales (id integer PRIMARY KEY, amount numeric(18,2), secret text);
INSERT INTO private_base.sales VALUES (1, 12.50, 'synthetic');
CREATE VIEW reporting.sales AS SELECT id, amount FROM private_base.sales;
CREATE VIEW "Reports.X"."Sales, ""Q""" AS SELECT amount AS "Amount" FROM private_base.sales;
CREATE VIEW reporting.empty_view AS SELECT id FROM private_base.sales WHERE false;
CREATE TABLE public.partial (id integer PRIMARY KEY, safe timestamptz, hidden text);
CREATE INDEX partial_safe_idx ON public.partial(safe);
CREATE INDEX partial_hidden_idx ON public.partial(hidden);
CREATE TABLE public.parent (tenant integer, id integer, PRIMARY KEY(tenant,id));
CREATE TABLE public.child (
 tenant integer, parent_id integer, id integer PRIMARY KEY,
 CONSTRAINT child_parent_fk FOREIGN KEY(tenant,parent_id) REFERENCES public.parent(tenant,id)
);
GRANT USAGE ON SCHEMA reporting, "Reports.X", public TO query_objects_reader;
GRANT SELECT ON reporting.sales, "Reports.X"."Sales, ""Q""", reporting.empty_view TO query_objects_reader;
GRANT SELECT(safe) ON public.partial TO query_objects_reader;
GRANT SELECT ON public.parent, public.child TO query_objects_reader;
-- private_base deliberately has no USAGE/SELECT grant for the reader.
-- Tests assume this role only on the isolated test connection.

-- Unsupported evidence must be omitted as a whole, even when the key is visible.
CREATE INDEX partial_expression_idx ON public.partial ((lower(hidden)));
CREATE INDEX partial_predicate_idx ON public.partial(safe) WHERE hidden IS NOT NULL;
CREATE INDEX partial_include_hidden_idx ON public.partial(safe) INCLUDE(hidden);
CREATE INDEX parent_expression_idx ON public.parent ((tenant + id));
CREATE INDEX parent_predicate_idx ON public.parent(id) WHERE tenant > 0;
CREATE INDEX parent_include_idx ON public.parent(tenant) INCLUDE(id);
CREATE MATERIALIZED VIEW reporting.materialized_sales AS SELECT id FROM private_base.sales;
GRANT SELECT ON reporting.materialized_sales TO query_objects_reader;
-- Neither production collector nor the read-only integration test executes this
-- fixture. Provision it manually only in the isolated query_objects_test DB.

-- R2/R3 fixture additions, still NOT executed by T02.
CREATE DOMAIN public.required_int AS integer NOT NULL;
CREATE TABLE public.default_domain (
 required public.required_int,
 quantity integer DEFAULT 1,
 created_at timestamptz DEFAULT CURRENT_TIMESTAMP
);
CREATE VIEW reporting.domain_view AS SELECT required, quantity FROM public.default_domain;
GRANT SELECT ON public.default_domain, reporting.domain_view TO query_objects_reader;
