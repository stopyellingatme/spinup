CREATE TABLE orders (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    reference text NOT NULL,
    total double precision NOT NULL,
    status text NOT NULL CHECK (status IN ('new', 'paid', 'shipped')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
