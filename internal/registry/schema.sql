-- Registry + vector storage schema. Idempotent: safe to run on every startup.
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS repos (
    id                text PRIMARY KEY,
    name              text NOT NULL,
    provider          text NOT NULL,
    remote_url        text NOT NULL,
    default_branch    text NOT NULL,
    embedding_model   text NOT NULL,
    embedding_dim     int  NOT NULL,
    active_version    int  NOT NULL DEFAULT 0,
    labels            jsonb NOT NULL DEFAULT '{}'::jsonb,
    commits_behind    int  NOT NULL DEFAULT 0,
    last_ingested_at  timestamptz,
    last_heartbeat_at timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- Partitioned vector store. One LIST partition per repo (created at registration).
CREATE TABLE IF NOT EXISTS code_embeddings (
    id          bigint GENERATED ALWAYS AS IDENTITY,
    repo_id     text NOT NULL,
    file_path   text NOT NULL,
    chunk_index int  NOT NULL,
    content     text NOT NULL,
    embedding   vector(768) NOT NULL,
    metadata    jsonb,
    version     int  NOT NULL DEFAULT 1,
    kind        text NOT NULL DEFAULT 'code',
    created_at  timestamptz NOT NULL DEFAULT now()
) PARTITION BY LIST (repo_id);

-- Additive migration for databases created before `kind` existed.
ALTER TABLE code_embeddings ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'code';
