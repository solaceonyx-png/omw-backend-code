CREATE TABLE IF NOT EXISTS identity_verification_sessions (
    id BIGSERIAL PRIMARY KEY,
    auth0_id VARCHAR(255) NOT NULL,
    stripe_session_id VARCHAR(255) NOT NULL UNIQUE,
    verification_flow VARCHAR(255),
    status VARCHAR(50) NOT NULL,
    last_error_code VARCHAR(100),
    last_error_reason TEXT,
    verified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_identity_verification_sessions_auth0_id ON identity_verification_sessions(auth0_id);
