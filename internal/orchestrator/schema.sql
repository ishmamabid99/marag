CREATE TABLE IF NOT EXISTS runs (
    id             text PRIMARY KEY,
    status         text NOT NULL,
    complexity     text NOT NULL,
    current_stage  text NOT NULL,
    spec           text NOT NULL,
    repo_ids       text[] NOT NULL DEFAULT '{}',
    qa_iterations  integer NOT NULL DEFAULT 0,
    failure_reason text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS run_audit_log (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id     text NOT NULL,
    stage      text NOT NULL,
    status     text NOT NULL,
    note       text,
    at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    key        text PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS compensations (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id       text NOT NULL,
    stage        text NOT NULL,
    action       text NOT NULL,
    git_provider text,
    params_json  jsonb,
    executed     boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS run_artifacts (
    run_id     text NOT NULL,
    stage      text NOT NULL,
    artifact   jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, stage)
);
