-- Add commute_id column to ride_matches table
ALTER TABLE ride_matches
ADD COLUMN commute_id BIGINT;

-- Optional: Create an index on commute_id for efficient lookups when filtering by commute
CREATE INDEX idx_ride_matches_commute_id ON ride_matches(commute_id);