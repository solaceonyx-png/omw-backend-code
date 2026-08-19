// Service rideshare
package rideshare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"encore.dev/beta/errs"
	"encore.dev/cron"
	"encore.dev/storage/sqldb"
	"github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateRideParams matches the frontend JSON payload
type CreateRideParams struct {
	PassengerID      int64    `json:"passengerId"`
	PickupLocName    string   `json:"pickupLocation"`
	RideDate         string   `json:"rideDate"` // e.g. "2026-07-14T00:00:00"
	RideTime         string   `json:"rideTime"`
	RepeatDays       []string `json:"repeatDays"`
	PickupLatitude   *float64 `json:"pickupLatitude,omitempty"`
	PickupLongitude  *float64 `json:"pickupLongitude,omitempty"`
	DropOffLocation  string   `json:"dropOffLocation"`
	DropOffLatitude  *float64 `json:"dropOffLatitude,omitempty"`
	DropOffLongitude *float64 `json:"dropOffLongitude,omitempty"`
}

type CreateRideResponse struct {
	RideRequestID string    `json:"ride_request_id"`
	Status        string    `json:"status"`
	RideTime      string    `json:"rideTime"`
	CreatedAt     time.Time `json:"created_at"`
}

//encore:api public path=/rides/create method=POST
func (s *Service) CreateRideRequest(ctx context.Context, params *CreateRideParams) (*CreateRideResponse, error) {
	fmt.Println("reached here")

	// 1. Check existing ride request count for this passenger
	var activeCount int64
	err := s.db.WithContext(ctx).
		Table("ride_requests").
		Where("passenger_id = ?", params.PassengerID).
		Count(&activeCount).Error

	if err != nil {
		return nil, fmt.Errorf("failed to check existing ride count: %w", err)
	}

	// 2. Reject request if user already has 3 or more ride requests
	if activeCount >= 3 {
		return nil, &errs.Error{
			Code:    errs.InvalidArgument,
			Message: "Maximum limit reached: You cannot have more than 3 active ride requests.",
		}
	}

	// 2. Build a raw data map to completely bypass GORM type-reflection errors
	rideData := map[string]interface{}{
		"passenger_id":      params.PassengerID,
		"ride_date":         params.RideDate,
		"ride_time":         params.RideTime,
		"repeat_days":       pq.StringArray(params.RepeatDays),
		"pickup_latitude":   params.PickupLatitude,
		"pickup_longitude":  params.PickupLongitude,
		"dropoff_latitude":  params.DropOffLatitude,
		"dropoff_longitude": params.DropOffLongitude,
		"pickup_location":   params.PickupLocName,
		"dropoff_location":  params.DropOffLocation,
		// "pickup_latitude":  params.PickupLatitude,
		// "pickup_longitude": params.PickupLongitude,
		"created_at": time.Now(),
		// ✅ PostGIS Geometry Columns mapping
		// Note: ST_MakePoint takes LONGITUDE first, then LATITUDE.
		// 4326 establishes the spatial reference system identifier (SRID) for GPS coordinates (WGS 84).
		"pickup_points": gorm.Expr(
			"ST_SetSRID(ST_MakePoint(?, ?), 4326)",
			params.PickupLongitude,
			params.PickupLatitude,
		),
		"dropoff_points": gorm.Expr(
			"ST_SetSRID(ST_MakePoint(?, ?), 4326)",
			params.DropOffLongitude,
			params.DropOffLatitude,
		),
	}

	fmt.Println(rideData)

	// 3. Define a lightweight destination to capture Postgres auto-generated fields
	var result struct {
		RideRequestID string
		CreatedAt     time.Time
		RideTime      string
	}

	// 4. Force GORM to point to the exact table string name, insert, and fetch IDs back
	// 4. Insert and read database-generated fields using GORM clauses
	err = s.db.WithContext(ctx).
		Table("ride_requests").
		Clauses(clause.Returning{
			Columns: []clause.Column{
				{Name: "id"},
				{Name: "created_at"},
				{Name: "ride_time"},
			},
		}).
		Create(&rideData). // Note: Pass a pointer to your map/struct
		Scan(&result).     // Safely maps the returning values into your result struct
		Error

	if err != nil {
		return nil, fmt.Errorf("failed to save ride request: %w", err)
	}

	return &CreateRideResponse{
		RideRequestID: result.RideRequestID,
		Status:        "created",
		CreatedAt:     result.CreatedAt,
		RideTime:      result.RideTime,
	}, nil
}

// Ride represents a full ride request object returned to the client.
type RideRequest struct {
	ID               int64     `json:"id"`
	PassengerID      int64     `json:"passengerId"`
	Status           string    `json:"status"` // 👈 Populated dynamically
	RideDate         string    `json:"rideDate"`
	RideTime         string    `json:"rideTime"`
	RepeatDays       []string  `json:"repeatDays"`
	PickupLocation   string    `json:"pickupLocation"`
	PickupLatitude   *float64  `json:"pickupLatitude,omitempty"`
	PickupLongitude  *float64  `json:"pickupLongitude,omitempty"`
	DropOffLocation  string    `json:"dropOffLocation"`
	DropOffLatitude  *float64  `json:"dropOffLatitude,omitempty"`
	DropOffLongitude *float64  `json:"dropOffLongitude,omitempty"`
	DropOffTime      string    `json:"dropOffTime"`
	CreatedAt        time.Time `json:"createdAt"`
}

type GetUserRidesResponse struct {
	Rides []RideRequest `json:"rides"`
}

// DeleteRideRequest Response struct (optional if empty, or return status)
type DeleteRideResponse struct {
	Success bool `json:"success"`
}

//encore:api public path=/rides/:rideID/user/:passengerID method=DELETE
func (s *Service) DeleteRideRequest(ctx context.Context, rideID int64, passengerID int64) (*DeleteRideResponse, error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Verify ownership and check if the ride request exists
		var count int64
		if err := tx.Table("ride_requests").
			Where("id = ? AND passenger_id = ?", rideID, passengerID).
			Count(&count).Error; err != nil {
			return fmt.Errorf("failed to verify ride request ownership: %w", err)
		}

		if count == 0 {
			return &errs.Error{
				Code:    errs.NotFound,
				Message: "Ride request not found or does not belong to this user.",
			}
		}

		// 2. Delete any associated record in ride_matches
		if err := tx.Table("ride_matches").
			Where("ride_request_id = ?", rideID).
			Delete(nil).Error; err != nil {
			return fmt.Errorf("failed to delete associated ride match: %w", err)
		}

		// 3. Delete the ride request itself
		result := tx.Table("ride_requests").
			Where("id = ? AND passenger_id = ?", rideID, passengerID).
			Delete(nil)

		if result.Error != nil {
			return fmt.Errorf("failed to delete ride request: %w", result.Error)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return &DeleteRideResponse{Success: true}, nil
}

//encore:api public path=/rides/user/:passengerID method=GET
func (s *Service) GetUserRides(ctx context.Context, passengerID int64) (*GetUserRidesResponse, error) {
	var rides []RideRequest

	// Uses COALESCE to set status to 'Matched' if a match record exists, otherwise 'Scheduled'
	err := s.db.WithContext(ctx).
		Table("ride_requests rr").
		Select(`
			rr.id,
			rr.passenger_id,
			COALESCE(rm.status, 'Scheduled') AS status,
			rr.ride_date,
			rr.ride_time,
			rr.pickup_location,
			rr.pickup_latitude,
			rr.pickup_longitude,
			rr.dropoff_location AS "drop_off_location",
			rr.dropoff_latitude,
			rr.dropoff_longitude,
			rr.dropoff_time,
			rr.created_at
		`).
		Joins("LEFT JOIN ride_matches rm ON rm.ride_request_id = rr.id").
		Where("rr.passenger_id = ?", passengerID).
		Order("rr.created_at DESC").
		Find(&rides).Error

	if err != nil {
		return nil, fmt.Errorf("failed to fetch user rides: %w", err)
	}

	return &GetUserRidesResponse{Rides: rides}, nil
}

type CreateCommuteParams struct {
	DriverID       int64   `json:"driverId"`
	StartLocation  string  `json:"startLocation"`
	StartLatitude  float64 `json:"startLatitude"`
	StartLongitude float64 `json:"startLongitude"`
	EndLocation    string  `json:"endLocation"`
	EndLatitude    float64 `json:"endLatitude"`
	EndLongitude   float64 `json:"endLongitude"`
	StartTime      string  `json:"startTime"`
	StartDate      string  `json:"startDate"`
}

type CreateCommuteResponse struct {
	CommuteID int64     `json:"commuteId"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

//encore:api public path=/commutes/create method=POST
func (s *Service) CreateCommute(ctx context.Context, params *CreateCommuteParams) (*CreateCommuteResponse, error) {
	if params.StartLocation == "" || params.EndLocation == "" || params.StartTime == "" {
		return nil, &errs.Error{
			Code:    errs.InvalidArgument,
			Message: "Start location, end location, and start time are required.",
		}
	}

	// 1. Check existing commute count for this driver
	var existingCount int64
	err := s.db.WithContext(ctx).
		Table("commutes").
		Where("driver_id = ?", params.DriverID).
		Count(&existingCount).Error

	if err != nil {
		return nil, fmt.Errorf("failed to check existing commute count: %w", err)
	}

	if existingCount >= 2 {
		return nil, &errs.Error{
			Code:    errs.FailedPrecondition,
			Message: "You have reached the maximum limit of 2 active commutes. Please delete an existing commute before creating a new one.",
		}
	}

	commuteData := map[string]interface{}{
		"driver_id":       params.DriverID,
		"start_location":  params.StartLocation,
		"start_latitude":  params.StartLatitude,
		"start_longitude": params.StartLongitude,
		"end_location":    params.EndLocation,
		"end_latitude":    params.EndLatitude,
		"end_longitude":   params.EndLongitude,
		"start_time":      params.StartTime,
		"start_date":      params.StartDate,
		"created_at":      time.Now(),
	}

	var result struct {
		ID        int64     `gorm:"column:id"`
		CreatedAt time.Time `gorm:"column:created_at"`
	}

	err = s.db.WithContext(ctx).
		Table("commutes").
		Clauses(clause.Returning{
			Columns: []clause.Column{
				{Name: "id"},
				{Name: "created_at"},
			},
		}).
		Create(&commuteData).
		Scan(&result).
		Error

	if err != nil {
		return nil, fmt.Errorf("failed to save commute: %w", err)
	}

	return &CreateCommuteResponse{
		CommuteID: result.ID,
		Status:    "created",
		CreatedAt: result.CreatedAt,
	}, nil
}

type Commute struct {
	ID             int64     `json:"id" gorm:"column:id"`
	DriverID       int64     `json:"driverId" gorm:"column:driver_id"`
	StartLocation  string    `json:"startLocation" gorm:"column:start_location"`
	StartLatitude  float64   `json:"startLatitude" gorm:"column:start_latitude"`
	StartLongitude float64   `json:"startLongitude" gorm:"column:start_longitude"`
	EndLocation    string    `json:"endLocation" gorm:"column:end_location"`
	EndLatitude    float64   `json:"endLatitude" gorm:"column:end_latitude"`
	EndLongitude   float64   `json:"endLongitude" gorm:"column:end_longitude"`
	StartTime      string    `json:"startTime" gorm:"column:start_time"`
	StartDate      time.Time `json:"startDate" gorm:"column:start_date"`
	CreatedAt      time.Time `json:"createdAt" gorm:"column:created_at"`
}

type GetDriverCommutesResponse struct {
	Commutes []Commute `json:"commutes"`
}

//encore:api public path=/commutes/driver/:driverID method=GET
func (s *Service) GetDriverCommutes(ctx context.Context, driverID int64) (*GetDriverCommutesResponse, error) {
	var commutes []Commute

	err := s.db.WithContext(ctx).
		Table("commutes").
		Where("driver_id = ?", driverID).
		Order("created_at DESC").
		Find(&commutes).Error

	if err != nil {
		return nil, fmt.Errorf("failed to fetch driver commutes: %w", err)
	}

	return &GetDriverCommutesResponse{Commutes: commutes}, nil
}

type DeleteCommuteParams struct {
	DriverID int64 `query:"driver_id"`
}

//encore:api public path=/commutes/:id method=DELETE
func (s *Service) DeleteCommute(ctx context.Context, id int64, params *DeleteCommuteParams) error {
	if params.DriverID == 0 {
		return &errs.Error{
			Code:    errs.InvalidArgument,
			Message: "driver_id query parameter is required",
		}
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Fetch the commute first to get its details (e.g. StartDate)
		var commute Commute
		err := tx.Table("commutes").
			Where("id = ? AND driver_id = ?", id, params.DriverID).
			First(&commute).Error

		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return &errs.Error{
					Code:    errs.NotFound,
					Message: "Commute not found or does not belong to this driver",
				}
			}
			return fmt.Errorf("failed to find commute: %w", err)
		}

		// 2. Find all ride_request_ids matched with this driver on the commute's date
		// Direct, exact match lookup by commute_id
		var matchedRequestIDs []int64
		err = tx.Table("ride_matches").
			Where("commute_id = ?", id).
			Pluck("ride_request_id", &matchedRequestIDs).Error
		if err != nil {
			return fmt.Errorf("failed to lookup matched ride requests: %w", err)
		}

		// 3. Reset matched ride_requests back to 'pending' status
		if len(matchedRequestIDs) > 0 {
			// err = tx.Table("ride_requests").
			// 	Where("id IN ?", matchedRequestIDs).
			// 	Update("status", "pending").Error

			// if err != nil {
			// 	return fmt.Errorf("failed to reset ride requests status: %w", err)
			// }

			// 4. Delete the ride_matches records
			err = tx.Table("ride_matches").
				Where("ride_request_id IN ?", matchedRequestIDs).
				Delete(nil).Error

			if err != nil {
				return fmt.Errorf("failed to delete ride matches: %w", err)
			}
		}

		// 5. Delete the commute record
		res := tx.Table("commutes").
			Where("id = ? AND driver_id = ?", id, params.DriverID).
			Delete(nil)

		if res.Error != nil {
			return fmt.Errorf("failed to delete commute: %w", res.Error)
		}

		if res.RowsAffected == 0 {
			return &errs.Error{
				Code:    errs.NotFound,
				Message: "Commute not found or already deleted",
			}
		}

		return nil
	})
}

type GetAllRideRequestsResponse struct {
	RideRequests []RideRequest `json:"rideRequests"`
}

//encore:api public path=/rides/requests method=GET
func (s *Service) GetAllRideRequests(ctx context.Context) (*GetAllRideRequestsResponse, error) {
	var requests []RideRequest

	// Fetch all pending ride requests for now
	err := s.db.WithContext(ctx).
		Table("ride_requests").
		// Where("status = ?", "pending").
		Order("created_at DESC").
		Find(&requests).Error

	if err != nil {
		return nil, fmt.Errorf("failed to fetch ride requests: %w", err)
	}

	return &GetAllRideRequestsResponse{RideRequests: requests}, nil
}

type GetMatchingRideRequestsResponse struct {
	Commute      Commute       `json:"commute"`
	RideRequests []RideRequest `json:"rideRequests"`
}

//encore:api public path=/commutes/matches/:commuteID method=GET
func (s *Service) GetCommuteMatches(ctx context.Context, commuteID int64) (*GetMatchingRideRequestsResponse, error) {
	// 1. Fetch the commute by ID
	var commute Commute
	err := s.db.WithContext(ctx).
		Table("commutes").
		Where("id = ?", commuteID).
		First(&commute).Error

	if err != nil {
		return nil, &errs.Error{
			Code:    errs.NotFound,
			Message: "Commute not found",
		}
	}

	// 2. Query ride requests matching:
	// - Unmatched (LEFT JOIN ride_matches WHERE ride_matches.ride_request_id IS NULL)
	// - Status = 'pending'
	// - Same date as the commute
	// - Within 30 minutes before or after commute.StartTime
	var matchingRequests []RideRequest

	err = s.db.WithContext(ctx).
		Table("ride_requests").
		Select("ride_requests.*, ride_requests.dropoff_location AS drop_off_location").
		Joins("LEFT JOIN ride_matches ON ride_matches.ride_request_id = ride_requests.id").
		Where("ride_matches.ride_request_id IS NULL"). // Find Rides that are not matched
		// Where("ride_requests.status = ?", "pending").
		Where("DATE(ride_requests.ride_date) = DATE(?)", commute.StartDate).
		Where(`
			ride_requests.ride_time::time BETWEEN 
			(?::time - INTERVAL '30 minutes') AND (?::time + INTERVAL '30 minutes')
		`, commute.StartTime, commute.StartTime).
		Order("ride_requests.ride_time ASC").
		Find(&matchingRequests).Error

	if err != nil {
		return nil, fmt.Errorf("failed to fetch matching ride requests: %w", err)
	}

	return &GetMatchingRideRequestsResponse{
		Commute:      commute,
		RideRequests: matchingRequests,
	}, nil
}

type CreateMatchParams struct {
	RideRequestID int64 `json:"rideRequestId"`
	CommuteID     int64 `json:"commuteId"`
}

type CreateMatchResponse struct {
	MatchID   int64     `json:"matchId"`
	Status    string    `json:"status"`
	MatchedAt time.Time `json:"matchedAt"`
}

//encore:api public path=/rides/match method=POST
func (s *Service) CreateRideMatch(ctx context.Context, params *CreateMatchParams) (*CreateMatchResponse, error) {
	if params.RideRequestID == 0 || params.CommuteID == 0 {
		return nil, &errs.Error{
			Code:    errs.InvalidArgument,
			Message: "rideRequestId and commuteId are required.",
		}
	}

	// 1. Fetch driver_id from the commute
	var commute Commute
	err := s.db.WithContext(ctx).
		Table("commutes").
		Where("id = ?", params.CommuteID).
		First(&commute).Error

	if err != nil {
		return nil, &errs.Error{
			Code:    errs.NotFound,
			Message: "Associated commute not found.",
		}
	}

	// 2. Check if driver already has 3 matches
	var matchCount int64
	err = s.db.WithContext(ctx).
		Table("ride_matches").
		Where("driver_id = ?", commute.DriverID).
		Count(&matchCount).Error

	if err != nil {
		return nil, fmt.Errorf("failed to check existing match count: %w", err)
	}

	if matchCount >= 3 {
		return nil, &errs.Error{
			Code:    errs.FailedPrecondition,
			Message: "Maximum limit reached: You cannot match with more than 3 riders.",
		}
	}

	// 3. Prepare and insert match record
	now := time.Now()
	matchData := map[string]interface{}{
		"ride_request_id": params.RideRequestID,
		"driver_id":       commute.DriverID,
		"commute_id":      params.CommuteID,
		"status":          "matched",
		"matched_at":      now,
		"updated_at":      now,
	}

	var result struct {
		ID int64 `gorm:"column:id"`
	}

	err = s.db.WithContext(ctx).
		Table("ride_matches").
		Clauses(clause.Returning{
			Columns: []clause.Column{
				{Name: "id"},
			},
		}).
		Create(&matchData).
		Scan(&result).
		Error

	if err != nil {
		return nil, fmt.Errorf("failed to create ride match (may already be matched): %w", err)
	}

	// // 4. Update ride request status to 'matched'
	// _ = s.db.WithContext(ctx).
	// 	Table("ride_requests").
	// 	Where("id = ?", params.RideRequestID).
	// 	Update("status", "matched").Error

	return &CreateMatchResponse{
		MatchID:   result.ID,
		Status:    "matched",
		MatchedAt: now,
	}, nil
}

type MatchedRideRequest struct {
	ID               int64     `json:"id" gorm:"column:id"`
	PassengerID      int64     `json:"passengerId" gorm:"column:passenger_id"`
	PickupLocation   string    `json:"pickupLocation" gorm:"column:pickup_location"`
	PickupLatitude   float64   `json:"pickupLatitude" gorm:"column:pickup_latitude"`
	PickupLongitude  float64   `json:"pickupLongitude" gorm:"column:pickup_longitude"`
	DropOffLocation  string    `json:"dropOffLocation" gorm:"column:drop_off_location"`
	DropOffLatitude  float64   `json:"dropOffLatitude" gorm:"column:drop_off_latitude"`
	DropOffLongitude float64   `json:"dropOffLongitude" gorm:"column:drop_off_longitude"`
	RideDate         time.Time `json:"rideDate" gorm:"column:ride_date"`
	RideTime         string    `json:"rideTime" gorm:"column:ride_time"`
	Status           string    `json:"status" gorm:"column:status"`
	MatchID          int64     `json:"matchId" gorm:"column:match_id"`
	MatchStatus      string    `json:"matchStatus" gorm:"column:match_status"`
	MatchedAt        time.Time `json:"matchedAt" gorm:"column:matched_at"`
}

type GetMatchedRidersResponse struct {
	Commute         Commute              `json:"commute"`
	MatchedRequests []MatchedRideRequest `json:"matchedRequests"`
}

//encore:api public path=/commutes/matched/:commuteID method=GET
func (s *Service) GetMatchedCommutes(ctx context.Context, commuteID int64) (*GetMatchedRidersResponse, error) {
	// 1. Get the commute by ID to grab its driver_id and details
	var commute Commute
	err := s.db.WithContext(ctx).
		Table("commutes").
		Where("id = ?", commuteID).
		First(&commute).Error

	if err != nil {
		return nil, &errs.Error{
			Code:    errs.NotFound,
			Message: "Commute not found",
		}
	}

	// 2. Get all ride requests joined with ride_matches for this driver
	var matchedRequests []MatchedRideRequest
	err = s.db.WithContext(ctx).
		Table("ride_requests").
		Select(`
			ride_requests.*,
			ride_requests.dropoff_location AS drop_off_location, 
			ride_matches.id AS match_id,
			ride_matches.status AS match_status,
			ride_matches.matched_at AS matched_at
		`).
		Joins("INNER JOIN ride_matches ON ride_matches.ride_request_id = ride_requests.id").
		Where("ride_matches.driver_id = ?", commute.DriverID).
		Where("ride_matches.commute_id = ?", commuteID).
		Order("ride_matches.matched_at DESC").
		Find(&matchedRequests).Error

	if err != nil {
		return nil, fmt.Errorf("failed to fetch matched ride requests: %w", err)
	}

	return &GetMatchedRidersResponse{
		Commute:         commute,
		MatchedRequests: matchedRequests,
	}, nil
}

// 1. Cron references the bare method name (no parentheses, no pointer prefixes)
var _ = cron.NewJob("check-upcoming-rides", cron.JobConfig{
	Title:    "Check Upcoming Rides",
	Every:    2 * cron.Minute,
	Endpoint: CheckUpcomingRides,
})

// 2. The method keeps its (s *Service) receiver so s.db and s.hub are available
//
//encore:api private
func (s *Service) CheckUpcomingRides(ctx context.Context) error {
	fmt.Println("⏰ Cron running: checking for upcoming rides...")
	now := time.Now()
	alertWindowStart := now.Add(15 * time.Minute)
	alertWindowEnd := now.Add(30 * time.Minute)

	type UpcomingMatch struct {
		MatchID       int64     `json:"matchId"`
		DriverID      int64     `json:"driverId"`
		PassengerID   int64     `json:"passengerId"`
		DepartureTime time.Time `json:"departureTime"`
	}

	var upcoming []UpcomingMatch

	// Query matches starting soon where a notification hasn't been sent yet
	err := s.db.WithContext(ctx).
		Table("ride_matches").
		Select(`
			ride_matches.id AS match_id,
			ride_matches.driver_id,
			ride_requests.passenger_id,
			commutes.start_date + commutes.start_time::time AS departure_time
		`).
		Joins("INNER JOIN ride_requests ON ride_requests.id = ride_matches.ride_request_id").
		Joins("INNER JOIN commutes ON commutes.id = ride_matches.commute_id").
		Where("ride_matches.status = ?", "matched").
		Where("ride_matches.start_reminder_sent = FALSE").
		Where("(commutes.start_date + commutes.start_time::time) BETWEEN ? AND ?", alertWindowStart, alertWindowEnd).
		Scan(&upcoming).Error

	fmt.Printf("📊 Matches found in window: %d\n", len(upcoming))
	if err != nil {
		return fmt.Errorf("failed to query upcoming rides: %w", err)
	}

	for _, match := range upcoming {
		// 1. Broadcast the event to driver and passenger
		s.publishNotification(match.DriverID, match.PassengerID, match.MatchID)

		// 2. Mark reminder as sent
		_ = s.db.WithContext(ctx).
			Table("ride_matches").
			Where("id = ?", match.MatchID).
			Update("start_reminder_sent", true).Error
	}

	return nil
}

// publishNotification handles the reminder event generated by the cron job.
// The ride service currently has no broker configured, so retain the event in
// the service's log until the notification transport is added.
// func (s *Service) publishNotification(driverID, passengerID, matchID int64) {
// 	fmt.Printf("ride start reminder: match_id=%d driver_id=%d passenger_id=%d\n", matchID, driverID, passengerID)
// }

// StreamNotifications streams real-time alerts to the Angular frontend via SSE
//
//encore:api public raw path=/notifications/stream method=GET
func (s *Service) StreamNotifications(w http.ResponseWriter, r *http.Request) {
	// Enable CORS if running frontend on a different port (e.g., localhost:4200)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	userIDStr := r.URL.Query().Get("userId")
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil || userID == 0 {
		http.Error(w, "Valid userId query parameter is required", http.StatusBadRequest)
		return
	}

	// Verify the response writer supports flushing
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Set SSE HTTP Headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Create a buffered channel for this client connection
	clientChan := make(chan RideAlertPayload, 10)
	s.hub.Register(userID, clientChan)

	// Send an initial connected handshake event
	fmt.Fprintf(w, "event: connected\ndata: {\"status\":\"connected\"}\n\n")
	flusher.Flush()

	// Clean up when client disconnects
	ctx := r.Context()
	defer s.hub.Unregister(userID, clientChan)

	// Heartbeat ticker to keep intermediate proxies/NATs alive
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Client closed connection / tab
			return

		case <-ticker.C:
			// Send comment line ping
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()

		case alert := <-clientChan:
			// Serialize and push SSE event
			data, _ := json.Marshal(alert)
			fmt.Fprintf(w, "event: ride-starting-soon\ndata: %s\n\n", string(data))
			flusher.Flush()
		}
	}
}

// RideAlertPayload is the JSON structure sent to Angular
type RideAlertPayload struct {
	MatchID   int64  `json:"matchId"`
	Message   string `json:"message"`
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
}

// Hub manages active SSE connections per user
type NotificationHub struct {
	mu      sync.RWMutex
	clients map[int64]map[chan RideAlertPayload]bool
}

func NewNotificationHub() *NotificationHub {
	return &NotificationHub{
		clients: make(map[int64]map[chan RideAlertPayload]bool),
	}
}

// Register adds a new client channel for a user
func (h *NotificationHub) Register(userID int64, ch chan RideAlertPayload) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.clients[userID]; !ok {
		h.clients[userID] = make(map[chan RideAlertPayload]bool)
	}
	h.clients[userID][ch] = true
}

// Unregister removes a client channel
func (h *NotificationHub) Unregister(userID int64, ch chan RideAlertPayload) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if userClients, ok := h.clients[userID]; ok {
		delete(userClients, ch)
		close(ch)
		if len(userClients) == 0 {
			delete(h.clients, userID)
		}
	}
}

// BroadcastToUser sends an alert to all active tabs/connections for a specific user
func (h *NotificationHub) BroadcastToUser(userID int64, payload RideAlertPayload) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if channels, ok := h.clients[userID]; ok {
		for ch := range channels {
			select {
			case ch <- payload:
			default:
				// Skip if client buffer is blocked
			}
		}
	}
}

// // GetNearbyDropoffs fetches all ride requests where the drop-off multipoint is within X miles.
// //
// //encore:api public path=/rides/nearby-dropoff
// func (s *Service) GetNearbyDropoffs(ctx context.Context, params *GetNearbyRequestsParams) (*GetNearbyRequestsResponse, error) {
// 	// Convert miles to meters for PostGIS geography calculations (1 mile = 1609.34 meters)
// 	// radiusMeters := params.RadiusMiles * 1609.34

// 	var rides []RideRequest

// 	err := s.db.WithContext(ctx).Raw(`
//     SELECT
//         id,
//         passenger_id,
//         ST_Distance(dropoff_points::geography, ST_MakePoint(?, ?)::geography) / 1609.34 AS distance_miles,
//         ST_AsGeoJSON(pickup_points)::jsonb AS pickup_points,
//         ST_AsGeoJSON(dropoff_points)::jsonb AS dropoff_points
//     FROM ride_requests
//     WHERE ST_DWithin(dropoff_points::geography, ST_MakePoint(?, ?)::geography, ?)
//       -- Use explicit CAST functions instead of the '::' shorthand
//       AND dropoff_time BETWEEN (CAST(? AS TIME) - INTERVAL '30 minutes')
//                            AND (CAST(? AS TIME) + INTERVAL '30 minutes')
//     ORDER BY distance_miles ASC
// `,
// 		params.Lng, params.Lat, // For ST_Distance (?, ?)
// 		params.Lng, params.Lat, // For ST_DWithin (?, ?)
// 		params.RadiusMiles*1609.34,             // For ST_DWithin radius (?)
// 		params.DropoffTime, params.DropoffTime, // For BETWEEN lower and upper bounds (?, ?)
// 	).Scan(&rides).Error

// 	if err != nil {
// 		return nil, err
// 	}

// 	return &GetNearbyRequestsResponse{RidesRequests: rides}, nil
// }

// Add the hub to your service struct (e.g., in service.go or rides.go)
//
//encore:service
type Service struct {
	db          *gorm.DB
	hub         *NotificationHub
	locationHub *LocationHub // 👈 Add Location Hub
}

// Initialize in initService or init function:
// func initService(db *gorm.DB) (*Service, error) {
//     return &Service{db: db, hub: NewNotificationHub()}, nil
// }

// publishNotification broadcasts alerts to both Driver and Passenger
func (s *Service) publishNotification(driverID int64, passengerID int64, matchID int64) {
	now := time.Now().Unix()

	// 1. Notify the driver
	driverAlert := RideAlertPayload{
		MatchID:   matchID,
		Message:   "Your scheduled commute starts in less than 30 minutes.",
		Type:      "ride_starting_soon",
		Timestamp: now,
	}
	s.hub.BroadcastToUser(driverID, driverAlert)

	// 2. Notify the passenger
	passengerAlert := RideAlertPayload{
		MatchID:   matchID,
		Message:   "Your driver is scheduled to depart in less than 30 minutes.",
		Type:      "ride_starting_soon",
		Timestamp: now,
	}
	s.hub.BroadcastToUser(passengerID, passengerAlert)
}

// // Define a database named 'rideshare', using the database migrations
// // in the "./migrations" folder.
var rideDB = sqldb.NewDatabase("ride", sqldb.DatabaseConfig{
	Migrations: "./migrations",
})

// initService initializes the service and starts the background worker
// func initService() (*Service, error) {
// 	db, err := gorm.Open(postgres.New(postgres.Config{
// 		Conn: rideDB.Stdlib(),
// 	}))
// 	if err != nil {
// 		return nil, err
// 	}

// 	svc := &Service{
// 		db:  db,
// 		hub: NewNotificationHub(),
// 	}

// 	// Start the background checker routine
// 	go svc.startUpcomingRidesWorker()

// 	return svc, nil
// }

func initService() (*Service, error) {
	db, err := gorm.Open(postgres.New(postgres.Config{
		Conn: rideDB.Stdlib(),
	}))
	if err != nil {
		return nil, err
	}

	svc := &Service{
		db:          db,
		hub:         NewNotificationHub(),
		locationHub: NewLocationHub(), // 👈 Initialize Location Hub
	}
	go svc.startUpcomingRidesWorker()

	return svc, nil
}

// Background loop that runs every 2 minutes
func (s *Service) startUpcomingRidesWorker() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	fmt.Println("🚀 Background upcoming rides worker started (running every 2m)...")

	for range ticker.C {
		ctx := context.Background()
		if err := s.CheckUpcomingRides(ctx); err != nil {
			fmt.Printf("❌ Error running CheckUpcomingRides: %v\n", err)
		}
	}
}

// LocationUpdate represents a single GPS telemetry ping
type LocationUpdate struct {
	MatchID   int64     `json:"matchId"`
	Latitude  float64   `json:"latitude"`
	Longitude float64   `json:"longitude"`
	Bearing   float64   `json:"bearing"`
	Speed     float64   `json:"speed"` // in km/h
	Timestamp time.Time `json:"timestamp"`
}

// RideCoordinatesResponse returns the static route anchors
type RideCoordinatesResponse struct {
	MatchID         int64   `json:"matchId"`
	StartLat        float64 `json:"startLat"`
	StartLng        float64 `json:"startLng"`
	EndLat          float64 `json:"endLat"`
	EndLng          float64 `json:"endLng"`
	PickupLocation  string  `json:"pickupLocation"`
	DropoffLocation string  `json:"dropoffLocation"`
	DriverID        int64   `json:"driverId"`
	PassengerID     int64   `json:"passengerId"`
}

// LocationHub manages live streaming per active match
type LocationHub struct {
	mu          sync.RWMutex
	subscribers map[int64]map[chan LocationUpdate]bool
	lastKnown   map[int64]LocationUpdate
}

func NewLocationHub() *LocationHub {
	return &LocationHub{
		subscribers: make(map[int64]map[chan LocationUpdate]bool),
		lastKnown:   make(map[int64]LocationUpdate),
	}
}

func (h *LocationHub) Subscribe(matchID int64, ch chan LocationUpdate) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.subscribers[matchID]; !ok {
		h.subscribers[matchID] = make(map[chan LocationUpdate]bool)
	}
	h.subscribers[matchID][ch] = true
}

func (h *LocationHub) Unsubscribe(matchID int64, ch chan LocationUpdate) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if subs, ok := h.subscribers[matchID]; ok {
		delete(subs, ch)
		close(ch)
		if len(subs) == 0 {
			delete(h.subscribers, matchID)
		}
	}
}

func (h *LocationHub) Broadcast(matchID int64, update LocationUpdate) {
	h.mu.Lock()
	h.lastKnown[matchID] = update
	h.mu.Unlock()

	h.mu.RLock()
	defer h.mu.RUnlock()

	if subs, ok := h.subscribers[matchID]; ok {
		for ch := range subs {
			select {
			case ch <- update:
			default:
				// Avoid blocking if client buffer is full
			}
		}
	}
}

func (h *LocationHub) GetLastKnown(matchID int64) (LocationUpdate, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	loc, exists := h.lastKnown[matchID]
	return loc, exists
}

//encore:api public path=/rides/matches/:matchID/coordinates method=GET
func (s *Service) GetMatchCoordinates(ctx context.Context, matchID int64) (*RideCoordinatesResponse, error) {
	var row struct {
		MatchID         int64
		DriverID        int64
		PassengerID     int64
		PickupLat       float64
		PickupLng       float64
		DropoffLat      float64
		DropoffLng      float64
		PickupLocation  string
		DropoffLocation string
	}

	err := s.db.WithContext(ctx).
		Table("ride_matches").
		Select(`
			ride_matches.id AS match_id,
			ride_matches.driver_id,
			ride_requests.passenger_id,
			ride_requests.pickup_latitude AS pickup_lat,
			ride_requests.pickup_longitude AS pickup_lng,
			ride_requests.dropoff_latitude AS dropoff_lat,
			ride_requests.dropoff_longitude AS dropoff_lng,
			ride_requests.pickup_location,
			ride_requests.dropoff_location
		`).
		Joins("INNER JOIN ride_requests ON ride_requests.id = ride_matches.ride_request_id").
		Where("ride_matches.id = ?", matchID).
		Scan(&row).Error

	if err != nil {
		return nil, fmt.Errorf("failed to load ride match: %w", err)
	}

	if row.MatchID == 0 {
		return nil, &errs.Error{
			Code:    errs.NotFound,
			Message: "Ride match not found.",
		}
	}

	return &RideCoordinatesResponse{
		MatchID:         row.MatchID,
		DriverID:        row.DriverID,
		PassengerID:     row.PassengerID,
		StartLat:        row.PickupLat,
		StartLng:        row.PickupLng,
		EndLat:          row.DropoffLat,
		EndLng:          row.DropoffLng,
		PickupLocation:  row.PickupLocation,
		DropoffLocation: row.DropoffLocation,
	}, nil
}

type UpdateLocationParams struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Bearing   float64 `json:"bearing"`
	Speed     float64 `json:"speed"`
}

//encore:api public path=/rides/matches/:matchID/location method=POST
func (s *Service) UpdateLocation(ctx context.Context, matchID int64, params *UpdateLocationParams) error {
	update := LocationUpdate{
		MatchID:   matchID,
		Latitude:  params.Latitude,
		Longitude: params.Longitude,
		Bearing:   params.Bearing,
		Speed:     params.Speed,
		Timestamp: time.Now().UTC(),
	}

	// Broadcast instantly to all subscribers watching this ride
	s.locationHub.Broadcast(matchID, update)
	return nil
}

//encore:api public raw path=/rides/matches/:matchID/track/stream method=GET
func (s *Service) StreamLiveLocation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	// Parse matchID from URL path query or route
	matchIDStr := r.URL.Query().Get("matchId")
	matchID, err := strconv.ParseInt(matchIDStr, 10, 64)
	if err != nil || matchID == 0 {
		http.Error(w, "Valid matchId is required", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	clientChan := make(chan LocationUpdate, 20)
	s.locationHub.Subscribe(matchID, clientChan)
	defer s.locationHub.Unsubscribe(matchID, clientChan)

	// If there is already a last known location, send it immediately
	if lastLoc, exists := s.locationHub.GetLastKnown(matchID); exists {
		data, _ := json.Marshal(lastLoc)
		fmt.Fprintf(w, "event: location\ndata: %s\n\n", string(data))
		flusher.Flush()
	}

	ctx := r.Context()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()

		case loc := <-clientChan:
			data, _ := json.Marshal(loc)
			fmt.Fprintf(w, "event: location\ndata: %s\n\n", string(data))
			flusher.Flush()
		}
	}
}
