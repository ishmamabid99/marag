CREATE TABLE IF NOT EXISTS mcp_principals (
    token_sha256 TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE mcp_principals ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
-- Scope: 'full' (default, back-compat for pre-scope tokens) or 'context' (read-only).
ALTER TABLE mcp_principals ADD COLUMN IF NOT EXISTS scope TEXT NOT NULL DEFAULT 'full';
