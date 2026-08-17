CREATE TABLE commutes (
    id BIGSERIAL PRIMARY KEY,
    driver_id BIGINT NOT NULL,
    start_location VARCHAR(255) NOT NULL,
    start_latitude DOUBLE PRECISION,
    start_longitude DOUBLE PRECISION,
    end_location VARCHAR(255) NOT NULL,
    end_latitude DOUBLE PRECISION,
    end_longitude DOUBLE PRECISION,
    start_time VARCHAR(20) NOT NULL,
    start_date DATE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_commutes_driver_id ON commutes(driver_id);