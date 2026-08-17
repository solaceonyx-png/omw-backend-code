-- CREATE TABLE users (
--     user_id BIGSERIAL PRIMARY KEY,
--     name VARCHAR(50) NOT NULL,
--     gender VARCHAR(50) NOT NULL,
--     pronouns VARCHAR(50) NOT NULL
-- );

CREATE EXTENSION IF NOT EXISTS postgis;

CREATE TABLE ride_requests (
-- 1. Correctly declare the Primary Key
    id BIGSERIAL PRIMARY KEY,
    
    -- 2. Use BIGINT here so it maps to user IDs instead of starting a new serial counter
    passenger_id BIGINT NOT NULL, 
    
    ride_date DATE NOT NULL,
    ride_time VARCHAR(5) NOT NULL,              -- e.g., "14:30"
    repeat_days TEXT[] NOT NULL,
    
    pickup_location TEXT NOT NULL,
    pickup_latitude DOUBLE PRECISION,
    pickup_longitude DOUBLE PRECISION,
    
    dropoff_location TEXT NOT NULL,
    dropoff_latitude DOUBLE PRECISION,
    dropoff_longitude DOUBLE PRECISION,
    
    -- 3. Add the structural PostGIS geometry fields required by your Go functions
    -- 4326 is the spatial reference ID (SRID) for standard WGS 84 GPS coordinates
    pickup_points GEOMETRY(Point, 4326),
    dropoff_points GEOMETRY(Point, 4326),
    
    -- 4. Match the column name from your Go code for nearby querying
    dropoff_time VARCHAR(5) NOT NULL DEFAULT '00:00',
    
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);
-- 5. Add spatial indexes so your distance searches execute instantly
CREATE INDEX idx_ride_requests_pickup ON ride_requests USING GIST (pickup_points);
CREATE INDEX idx_ride_requests_dropoff ON ride_requests USING GIST (dropoff_points);
