package auth

import (
	"context"

	"encore.dev/beta/auth"
	"encore.dev/beta/errs"
)

type MeResponse struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
}

// //encore:api auth method=GET path=/auth/user/me
func GetMe(ctx context.Context) (*MeResponse, error) {
	uid, loggedIn := auth.UserID()
	if !loggedIn {
		return nil, &errs.Error{
			Code:    errs.Unauthenticated,
			Message: "Session invalid or expired.",
		}
	}

	rawContextData := auth.Data()
	authData, ok := rawContextData.(*AuthData)
	if !ok {
		return nil, &errs.Error{
			Code:    errs.Internal,
			Message: "Failed to parse context data.",
		}
	}

	return &MeResponse{
		UserID: string(uid),
		Email:  authData.Email,
	}, nil
}
