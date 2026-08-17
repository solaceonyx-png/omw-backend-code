
CREATE TABLE ride_matches (
    id BIGSERIAL PRIMARY KEY,
    
    -- Foreign key to ride_requests. 
    -- UNIQUE constraint prevents a ride request from being matched multiple times simultaneously.
    ride_request_id BIGINT NOT NULL UNIQUE REFERENCES ride_requests(id) ON DELETE CASCADE,
    
    -- The driver who accepted or was assigned to the ride
    driver_id BIGINT NOT NULL,
    
    -- Lifecycle status of the match: 'matched', 'accepted', 'en_route', 'completed', 'cancelled'
    status VARCHAR(20) NOT NULL DEFAULT 'matched',
    
    matched_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Indexes to make lookups fast when fetching a driver's matches or a ride's match status
CREATE INDEX idx_ride_matches_driver_id ON ride_matches(driver_id);
CREATE INDEX idx_ride_matches_ride_request_id ON ride_matches(ride_request_id);