CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    auth0_id VARCHAR(255) NOT NULL UNIQUE,
    email VARCHAR(255),
    first_name VARCHAR(100),
    last_name VARCHAR(100),
    -- phone_number VARCHAR(50),
    avatar_url TEXT,
    is_driver BOOLEAN DEFAULT FALSE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Index for fast Auth0 ID resolution on every request
CREATE INDEX IF NOT EXISTS idx_users_auth0_id ON users(auth0_id);

-- Optional: Link existing ride tables to users table
-- ALTER TABLE ride_requests 
--   ADD CONSTRAINT fk_ride_requests_passenger 
--   FOREIGN KEY (passenger_id) REFERENCES users(id) ON DELETE CASCADE;