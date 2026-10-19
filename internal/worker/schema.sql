CREATE TABLE IF NOT EXISTS checkpoints (
    run_id        text NOT NULL,
    stage         text NOT NULL,
    attempt       int  NOT NULL,
    status        text NOT NULL,
    reason        text,
    artifact      jsonb,
    input_tokens  int  NOT NULL DEFAULT 0,
    output_tokens int  NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, stage, attempt)
);

-- The reason column was added after this table was first shipped; CREATE
-- TABLE IF NOT EXISTS is a no-op against a pre-existing table, so guard the
-- column addition separately for deployments upgrading in place.
ALTER TABLE checkpoints ADD COLUMN IF NOT EXISTS reason text;
