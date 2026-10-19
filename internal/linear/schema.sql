CREATE TABLE IF NOT EXISTS linear_runs (
    run_id     text PRIMARY KEY,
    issue_id   text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
