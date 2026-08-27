package rideshare

// Stripe Identity document verification: creating verification sessions
// against a dashboard-configured Verification Flow, exposing status to the
// frontend, and consuming Stripe's webhook to keep that status up to date.
//
// Lives in the ride service (same DB as `users`) rather than as its own
// service/database — there's no need for that separation yet, and keeping
// it here lets verification sessions reference users.id directly.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"encore.dev/beta/errs"
	"encore.dev/config"
	stripe "github.com/stripe/stripe-go/v78"
	"github.com/stripe/stripe-go/v78/identity/verificationsession"
	"github.com/stripe/stripe-go/v78/webhook"
	"gorm.io/gorm"
)

type RideConfig struct {
	// VerificationFlowID is the ID of the Verification Flow created in the
	// Stripe dashboard (Identity > Verification flows), e.g. "vf_...".
	VerificationFlowID config.String
	// ReturnURL is where Stripe redirects the user after they complete (or
	// abandon) the hosted verification flow.
	ReturnURL config.String
}

var cfg = config.Load[*RideConfig]()

var secrets struct {
	// StripeSecretKey is the Stripe API secret key (sk_test_... / sk_live_...).
	StripeSecretKey string
	// StripeWebhookSecret is the signing secret for the Identity webhook
	// endpoint (whsec_...), from Stripe dashboard > Webhooks.
	StripeWebhookSecret string
}

// VerificationSession tracks a Stripe Identity verification attempt for a user.
type VerificationSession struct {
	ID               int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID           int64      `gorm:"column:user_id;not null;index" json:"userId"`
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

type CreateVerificationSessionResponse struct {
	SessionID string `json:"sessionId"`
	// URL is the short-lived (48h), single-use Stripe-hosted page to redirect
	// the user to. Use this for the hosted redirect flow.
	URL string `json:"url,omitempty"`
	// ClientSecret is the short-lived (24h), single-use secret for
	// Stripe.js's embedded verification modal (stripe.verifyIdentity()).
	// Don't store, log, or embed it anywhere other than immediately handing
	// it to Stripe.js.
	ClientSecret string `json:"clientSecret,omitempty"`
	// Both URL and ClientSecret are empty when the user is already verified.
	Status string `json:"status"`
}

// CreateVerificationSession starts (or reuses) an identity verification for
// the current user against the dashboard-configured Verification Flow.
//
//encore:api auth method=POST path=/identity/verification-sessions
func (s *Service) CreateVerificationSession(ctx context.Context) (*CreateVerificationSessionResponse, error) {
	user, err := s.GetOrCreateUser(ctx)
	if err != nil {
		return nil, &errs.Error{
			Code:    errs.Internal,
			Message: "Failed to resolve user account",
		}
	}

	// Don't spend a new verification on a user who is already verified.
	var existing VerificationSession
	err = s.db.WithContext(ctx).
		Where("user_id = ?", user.ID).
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

	fmt.Println(user.Email)
	params := &stripe.IdentityVerificationSessionParams{
		VerificationFlow: stripe.String(cfg.VerificationFlowID()),
		ReturnURL:        stripe.String(cfg.ReturnURL()),
		ProvidedDetails: &stripe.IdentityVerificationSessionProvidedDetailsParams{
			Email: stripe.String(user.Email),
		},
	}
	params.AddMetadata("user_id", fmt.Sprintf("%d", user.ID))

	session, err := verificationsession.New(params)
	if err != nil {
		return nil, &errs.Error{
			Code:    errs.Internal,
			Message: "Failed to create Stripe Identity verification session.",
		}
	}

	record := VerificationSession{
		UserID:           user.ID,
		StripeSessionID:  session.ID,
		VerificationFlow: session.VerificationFlow,
		Status:           string(session.Status),
	}
	if err := s.db.WithContext(ctx).Create(&record).Error; err != nil {
		return nil, fmt.Errorf("failed to save verification session: %w", err)
	}

	return &CreateVerificationSessionResponse{
		SessionID:    session.ID,
		URL:          session.URL,
		ClientSecret: session.ClientSecret,
		Status:       string(session.Status),
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
	user, err := s.GetOrCreateUser(ctx)
	if err != nil {
		return nil, &errs.Error{
			Code:    errs.Internal,
			Message: "Failed to resolve user account",
		}
	}

	var record VerificationSession
	err = s.db.WithContext(ctx).
		Where("user_id = ?", user.ID).
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
	fmt.Println("HandleStripeWebhook: request received")
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes)

	payload, err := io.ReadAll(r.Body)
	if err != nil {
		fmt.Println("HandleStripeWebhook: body read error:", err)
		http.Error(w, "request body too large or unreadable", http.StatusBadRequest)
		return
	}

	// IgnoreAPIVersionMismatch: stripe-go pins an expected event API version
	// and refuses otherwise-validly-signed events sent at a newer one. We
	// only read a handful of long-stable fields below (id, status,
	// last_error, verification_flow, metadata), so a version drift there is
	// low-risk. Signature validation still runs and must pass regardless.
	event, err := webhook.ConstructEventWithOptions(payload, r.Header.Get("Stripe-Signature"), secrets.StripeWebhookSecret,
		webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true})
	if err != nil {
		fmt.Println("HandleStripeWebhook: signature verification error:", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	fmt.Println("HandleStripeWebhook: verified event type:", event.Type)

	switch event.Type {
	case stripe.EventTypeIdentityVerificationSessionVerified,
		stripe.EventTypeIdentityVerificationSessionRequiresInput,
		stripe.EventTypeIdentityVerificationSessionProcessing,
		stripe.EventTypeIdentityVerificationSessionCanceled:

		var session stripe.IdentityVerificationSession
		if err := json.Unmarshal(event.Data.Raw, &session); err != nil {
			fmt.Println("HandleStripeWebhook: payload unmarshal error:", err)
			http.Error(w, "malformed event payload", http.StatusBadRequest)
			return
		}

		if err := s.applyVerificationSessionUpdate(r.Context(), &session); err != nil {
			fmt.Println("HandleStripeWebhook: apply update error:", err)
			http.Error(w, "failed to process event", http.StatusInternalServerError)
			return
		}

		fmt.Println("HandleStripeWebhook: applied update for session:", session.ID)
	}

	w.WriteHeader(http.StatusOK)
}

// applyVerificationSessionUpdate upserts the local record for a Stripe
// verification session based on a webhook event payload.
func (s *Service) applyVerificationSessionUpdate(ctx context.Context, session *stripe.IdentityVerificationSession) error {
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
		userID, ok := verificationSessionUserID(session)
		if !ok {
			return fmt.Errorf("verification session %s has no known user_id, cannot record", session.ID)
		}

		record := VerificationSession{
			UserID:           userID,
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

	if session.Status == stripe.IdentityVerificationSessionStatusVerified {
		if userID, ok := verificationSessionUserID(session); ok {
			err := s.db.WithContext(ctx).Model(&User{}).
				Where("id = ?", userID).
				Updates(map[string]interface{}{
					"is_identity_verified": true,
					"identity_verified_at": time.Now().UTC(),
				}).Error
			if err != nil {
				return fmt.Errorf("failed to mark user %d identity-verified: %w", userID, err)
			}
		}
	}

	return nil
}

// verificationSessionUserID extracts the user_id we attach as metadata when
// creating a verification session.
func verificationSessionUserID(session *stripe.IdentityVerificationSession) (int64, bool) {
	var userID int64
	if _, err := fmt.Sscanf(session.Metadata["user_id"], "%d", &userID); err != nil || userID == 0 {
		return 0, false
	}
	return userID, true
}
