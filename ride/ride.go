// Service rideshare
package rideshare

import (
	"context"
	"fmt"
	"time"

	"encore.dev/beta/errs"
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
type Ride struct {
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
	Rides []Ride `json:"rides"`
}

// DeleteRideRequest Response struct (optional if empty, or return status)
type DeleteRideResponse struct {
	Success bool `json:"success"`
}

//encore:api public path=/rides/:rideID/user/:passengerID method=DELETE
func (s *Service) DeleteRideRequest(ctx context.Context, rideID int64, passengerID int64) (*DeleteRideResponse, error) {
	// Execute SQL DELETE with composite WHERE clause verifying ownership
	result := s.db.WithContext(ctx).
		Table("ride_requests").
		Where("id = ? AND passenger_id = ?", rideID, passengerID).
		Delete(nil)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to delete ride request: %w", result.Error)
	}

	// Returns 0 affected rows if ID doesn't exist OR doesn't belong to passengerID
	if result.RowsAffected == 0 {
		return nil, &errs.Error{
			Code:    errs.NotFound,
			Message: "Ride request not found or does not belong to this user.",
		}
	}

	return &DeleteRideResponse{Success: true}, nil
}

//encore:api public path=/rides/user/:passengerID method=GET
func (s *Service) GetUserRides(ctx context.Context, passengerID int64) (*GetUserRidesResponse, error) {
	var rides []Ride

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

	result := s.db.WithContext(ctx).
		Table("commutes").
		Where("id = ? AND driver_id = ?", id, params.DriverID).
		Delete(nil)

	if result.Error != nil {
		return fmt.Errorf("failed to delete commute: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return &errs.Error{
			Code:    errs.NotFound,
			Message: "Commute not found or does not belong to this driver",
		}
	}

	return nil
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

//encore:service
type Service struct {
	db *gorm.DB
}

// // Define a database named 'rideshare', using the database migrations
// // in the "./migrations" folder.
var rideDB = sqldb.NewDatabase("ride", sqldb.DatabaseConfig{
	Migrations: "./migrations",
})

// initService initializes the site service.
// It is automatically called by Encore on service startup.
func initService() (*Service, error) {
	db, err := gorm.Open(postgres.New(postgres.Config{
		Conn: rideDB.Stdlib(),
	}))
	if err != nil {
		return nil, err
	}

	return &Service{db: db}, nil
}
