-- +goose Up
-- spinup:safe
-- The billing module of ADR 0032. A user has zero or one Stripe customer.
-- Deleting a user deletes these rows. The Stripe customer stays in Stripe.
CREATE TABLE billing_customers (
    user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    customer_id text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- The last state of each subscription that the sync fetched from Stripe.
CREATE TABLE billing_subscriptions (
    id text PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    customer_id text NOT NULL,
    status text NOT NULL,
    price_lookup_key text NOT NULL DEFAULT '',
    plan text NOT NULL DEFAULT '',
    current_period_end timestamptz,
    cancel_at_period_end boolean NOT NULL DEFAULT false,
    trial_end timestamptz,
    synced_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX billing_subscriptions_user_id_idx ON billing_subscriptions (user_id);

-- The effective plan of each user. A user with no row has the default plan.
CREATE TABLE billing_entitlements (
    user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    plan text NOT NULL,
    features text[] NOT NULL DEFAULT '{}',
    status text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One row for each webhook event, for dedupe. The sync job sets processed_at.
CREATE TABLE billing_events (
    event_id text PRIMARY KEY,
    type text NOT NULL,
    customer_id text NOT NULL DEFAULT '',
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    error text NOT NULL DEFAULT ''
);

-- The API key calls of each user and UTC day that the usage job sent to Stripe.
CREATE TABLE billing_usage_reports (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    day date NOT NULL,
    reported bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, day)
);

-- The audit module records the customer, subscription, and entitlement changes,
-- but not the events and the usage reports. The block acts only when the audit
-- functions exist (the audit migration ran first). When the audit module comes
-- later, the doctor check billing-wired prints the attach statements
-- (ADR 0032 section 7.1).
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regprocedure('public.audit_row_change()') IS NOT NULL THEN
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.billing_customers') AND tgname = 'billing_customers_audit') THEN
            CREATE TRIGGER billing_customers_audit AFTER INSERT OR UPDATE OR DELETE ON public.billing_customers
                FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'user_id');
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.billing_customers') AND tgname = 'billing_customers_audit_truncate') THEN
            CREATE TRIGGER billing_customers_audit_truncate AFTER TRUNCATE ON public.billing_customers
                FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.billing_subscriptions') AND tgname = 'billing_subscriptions_audit') THEN
            CREATE TRIGGER billing_subscriptions_audit AFTER INSERT OR UPDATE OR DELETE ON public.billing_subscriptions
                FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'id');
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.billing_subscriptions') AND tgname = 'billing_subscriptions_audit_truncate') THEN
            CREATE TRIGGER billing_subscriptions_audit_truncate AFTER TRUNCATE ON public.billing_subscriptions
                FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.billing_entitlements') AND tgname = 'billing_entitlements_audit') THEN
            CREATE TRIGGER billing_entitlements_audit AFTER INSERT OR UPDATE OR DELETE ON public.billing_entitlements
                FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'user_id');
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.billing_entitlements') AND tgname = 'billing_entitlements_audit_truncate') THEN
            CREATE TRIGGER billing_entitlements_audit_truncate AFTER TRUNCATE ON public.billing_entitlements
                FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();
        END IF;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE billing_usage_reports;
DROP TABLE billing_events;
DROP TABLE billing_entitlements;
DROP TABLE billing_subscriptions;
DROP TABLE billing_customers;
