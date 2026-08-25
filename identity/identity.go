// Service identity implements Stripe Identity document verification:
// creating verification sessions against a dashboard-configured Verification
// Flow, exposing status to the frontend, and consuming Stripe's webhook to
// keep that status up to date.
package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	myauth "encore.app/auth"
	"encore.dev/beta/auth"
	"encore.dev/beta/errs"
	"encore.dev/config"
	"encore.dev/storage/sqldb"
	stripe "github.com/stripe/stripe-go/v78"
	"github.com/stripe/stripe-go/v78/identity/verificationsession"
	"github.com/stripe/stripe-go/v78/webhook"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type IdentityConfig struct {
	// VerificationFlowID is the ID of the Verification Flow created in the
	// Stripe dashboard (Identity > Verification flows), e.g. "vf_...".
	VerificationFlowID config.String
	// ReturnURL is where Stripe redirects the user after they complete (or
	// abandon) the hosted verification flow.
	ReturnURL config.String
}

var cfg = config.Load[*IdentityConfig]()

var secrets struct {
	// StripeSecretKey is the Stripe API secret key (sk_test_... / sk_live_...).
	StripeSecretKey string
	// StripeWebhookSecret is the signing secret for the Identity webhook
	// endpoint (whsec_...), from Stripe dashboard > Webhooks.
	StripeWebhookSecret string
}

//encore:service
type Service struct {
	db *gorm.DB
}

var identityDB = sqldb.NewDatabase("identity", sqldb.DatabaseConfig{
	Migrations: "./migrations",
})

func initService() (*Service, error) {
	stripe.Key = secrets.StripeSecretKey

	db, err := gorm.Open(postgres.New(postgres.Config{
		Conn: identityDB.Stdlib(),
	}))
	if err != nil {
		return nil, err
	}

	return &Service{db: db}, nil
}

// VerificationSession tracks a Stripe Identity verification attempt for a user.
type VerificationSession struct {
	ID               int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	Auth0ID          string     `gorm:"column:auth0_id;type:varchar(255);not null;index" json:"auth0Id"`
	StripeSessionID  string     `gorm:"column:stripe_session_id;type:varchar(255);uniqueIndex;not null" json:"stripeSessionId"`
	VerificationFlow string     `gorm:"column:verification_flow;type:varchar(255)" json:"verificationFlow"`
	Status           string     `gorm:"column:status;type:varchar(50);not null" json:"status"`
	LastErrorCode    string     `gorm:"column:last_error_code;type:varchar(100)" json:"lastErrorCode,omitempty"`
	LastErrorReason  string     `gorm:"column:last_error_reason;type:text" json:"lastErrorReason,omitempty"`
	VerifiedAt       *time.Time `gorm:"column:verified_at" json:"verifiedAt,omitempty"`
	CreatedAt        time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt        time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (VerificationSession) TableName() string { return "identity_verification_sessions" }

// currentUser resolves the authenticated Auth0 user data from context.
func currentUser() (*myauth.UserData, error) {
	userData, ok := auth.Data().(*myauth.UserData)
	if !ok || userData == nil {
		return nil, &errs.Error{Code: errs.Unauthenticated, Message: "missing or invalid user auth data"}
	}
	return userData, nil
}

type CreateVerificationSessionResponse struct {
	SessionID string `json:"sessionId"`
	// URL is the short-lived (48h), single-use Stripe-hosted page to redirect
	// the user to. Empty when the user is already verified.
	URL    string `json:"url,omitempty"`
	Status string `json:"status"`
}

// CreateVerificationSession starts (or reuses) an identity verification for
// the current user against the dashboard-configured Verification Flow.
//
//encore:api auth method=POST path=/identity/verification-sessions
func (s *Service) CreateVerificationSession(ctx context.Context) (*CreateVerificationSessionResponse, error) {
	userData, err := currentUser()
	if err != nil {
		return nil, err
	}

	// Don't spend a new verification on a user who is already verified.
	var existing VerificationSession
	err = s.db.WithContext(ctx).
		Where("auth0_id = ?", userData.Auth0ID).
		Order("created_at DESC").
		First(&existing).Error
	if err == nil && existing.Status == string(stripe.IdentityVerificationSessionStatusVerified) {
		return &CreateVerificationSessionResponse{
			SessionID: existing.StripeSessionID,
			Status:    existing.Status,
		}, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("failed to look up existing verification session: %w", err)
	}

	params := &stripe.IdentityVerificationSessionParams{
		VerificationFlow: stripe.String(cfg.VerificationFlowID()),
		ReturnURL:        stripe.String(cfg.ReturnURL()),
		ProvidedDetails: &stripe.IdentityVerificationSessionProvidedDetailsParams{
			Email: stripe.String(userData.Email),
		},
	}
	params.AddMetadata("auth0_id", userData.Auth0ID)

	session, err := verificationsession.New(params)
	if err != nil {
		return nil, &errs.Error{
			Code:    errs.Internal,
			Message: "Failed to create Stripe Identity verification session.",
		}
	}

	record := VerificationSession{
		Auth0ID:          userData.Auth0ID,
		StripeSessionID:  session.ID,
		VerificationFlow: session.VerificationFlow,
		Status:           string(session.Status),
	}
	if err := s.db.WithContext(ctx).Create(&record).Error; err != nil {
		return nil, fmt.Errorf("failed to save verification session: %w", err)
	}

	return &CreateVerificationSessionResponse{
		SessionID: session.ID,
		URL:       session.URL,
		Status:    string(session.Status),
	}, nil
}

type VerificationStatusResponse struct {
	Status          string     `json:"status"`
	VerifiedAt      *time.Time `json:"verifiedAt,omitempty"`
	LastErrorReason string     `json:"lastErrorReason,omitempty"`
}

// GetVerificationStatus returns the current user's latest verification
// status, as tracked by the webhook — no live Stripe call is made.
//
//encore:api auth method=GET path=/identity/verification-status
func (s *Service) GetVerificationStatus(ctx context.Context) (*VerificationStatusResponse, error) {
	userData, err := currentUser()
	if err != nil {
		return nil, err
	}

	var record VerificationSession
	err = s.db.WithContext(ctx).
		Where("auth0_id = ?", userData.Auth0ID).
		Order("created_at DESC").
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &VerificationStatusResponse{Status: "unverified"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to look up verification status: %w", err)
	}

	return &VerificationStatusResponse{
		Status:          record.Status,
		VerifiedAt:      record.VerifiedAt,
		LastErrorReason: record.LastErrorReason,
	}, nil
}

// maxWebhookBodyBytes bounds how much of a webhook request body we'll read,
// since this is unauthenticated, externally-reachable input.
const maxWebhookBodyBytes = 65536

// HandleStripeWebhook consumes identity.verification_session.* events from
// Stripe and updates the corresponding VerificationSession record.
//
//encore:api public raw method=POST path=/identity/webhook
func (s *Service) HandleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes)

	payload, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "request body too large or unreadable", http.StatusBadRequest)
		return
	}

	event, err := webhook.ConstructEvent(payload, r.Header.Get("Stripe-Signature"), secrets.StripeWebhookSecret)
	if err != nil {
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	switch event.Type {
	case stripe.EventTypeIdentityVerificationSessionVerified,
		stripe.EventTypeIdentityVerificationSessionRequiresInput,
		stripe.EventTypeIdentityVerificationSessionProcessing,
		stripe.EventTypeIdentityVerificationSessionCanceled:

		var session stripe.IdentityVerificationSession
		if err := json.Unmarshal(event.Data.Raw, &session); err != nil {
			http.Error(w, "malformed event payload", http.StatusBadRequest)
			return
		}

		if err := s.applySessionUpdate(r.Context(), &session); err != nil {
			http.Error(w, "failed to process event", http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
}

// applySessionUpdate upserts the local record for a Stripe verification
// session based on a webhook event payload.
func (s *Service) applySessionUpdate(ctx context.Context, session *stripe.IdentityVerificationSession) error {
	updates := map[string]interface{}{
		"status":            string(session.Status),
		"last_error_code":   "",
		"last_error_reason": "",
		"updated_at":        time.Now().UTC(),
	}
	if session.LastError != nil {
		updates["last_error_code"] = string(session.LastError.Code)
		updates["last_error_reason"] = session.LastError.Reason
	}
	if session.Status == stripe.IdentityVerificationSessionStatusVerified {
		updates["verified_at"] = time.Now().UTC()
	}

	result := s.db.WithContext(ctx).
		Model(&VerificationSession{}).
		Where("stripe_session_id = ?", session.ID).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to update verification session %s: %w", session.ID, result.Error)
	}

	if result.RowsAffected == 0 {
		// The session wasn't created through CreateVerificationSession (e.g.
		// started directly from the Stripe dashboard) — record it now.
		record := VerificationSession{
			Auth0ID:          session.Metadata["auth0_id"],
			StripeSessionID:  session.ID,
			VerificationFlow: session.VerificationFlow,
			Status:           string(session.Status),
		}
		if session.LastError != nil {
			record.LastErrorCode = string(session.LastError.Code)
			record.LastErrorReason = session.LastError.Reason
		}
		if err := s.db.WithContext(ctx).Create(&record).Error; err != nil {
			return fmt.Errorf("failed to insert verification session %s: %w", session.ID, err)
		}
	}

	return nil
}
