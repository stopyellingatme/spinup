CREATE TABLE shipments (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tracking text NOT NULL,
    shipped_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
