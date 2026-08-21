package auth

import (
	"context"
	"net/url"
	"strings"

	"encore.dev/beta/auth"
	"encore.dev/beta/errs"
	"github.com/golang-jwt/jwt/v5"
)

// Service struct definition.
// Learn more: encore.dev/docs/primitives/services-and-apis/service-structs
//
//encore:service
type Service struct {
	auth *Authenticator
}

// initService is automatically called by Encore when the service starts up.
func initService() (*Service, error) {
	authenticator, err := New()
	if err != nil {
		return nil, err
	}
	return &Service{auth: authenticator}, nil
}

type LoginResponse struct {
	State       string `json:"state"`
	AuthCodeURL string `json:"auth_code_url"`
}

//encore:api public method=POST path=/auth/login
func (s *Service) Login(ctx context.Context) (*LoginResponse, error) {
	state, err := generateRandomState()
	if err != nil {
		return nil, &errs.Error{
			Code:    errs.Internal,
			Message: err.Error(),
		}
	}

	return &LoginResponse{
		State: state,
		// add the audience to the auth code url
		AuthCodeURL: s.auth.AuthCodeURL(state),
	}, nil
}

type CallbackRequest struct {
	Code string `json:"code"`
}

type CallbackResponse struct {
	Token string `json:"token"`
}

//encore:api public method=POST path=/auth/callback
func (s *Service) Callback(
	ctx context.Context,
	req *CallbackRequest,
) (*CallbackResponse, error) {

	// Exchange an authorization code for a token.
	token, err := s.auth.Exchange(ctx, req.Code)
	if err != nil {
		return nil, &errs.Error{
			Code:    errs.PermissionDenied,
			Message: "Failed to convert an authorization code into a token.",
		}
	}

	idToken, err := s.auth.VerifyIDToken(ctx, token)
	if err != nil {
		return nil, &errs.Error{
			Code:    errs.Internal,
			Message: "Failed to verify ID Token.",
		}
	}

	var profile map[string]interface{}
	if err := idToken.Claims(&profile); err != nil {
		return nil, &errs.Error{
			Code:    errs.Internal,
			Message: err.Error(),
		}
	}

	return &CallbackResponse{
		Token: token.Extra("id_token").(string),
	}, nil
}

type LogoutResponse struct {
	RedirectURL string `json:"redirect_url"`
}

//encore:api public method=GET path=/auth/logout
func (s *Service) Logout(ctx context.Context) (*LogoutResponse, error) {
	logoutUrl, err := url.Parse("https://" + cfg.Domain() + "/v2/logout")
	if err != nil {
		return nil, &errs.Error{
			Code:    errs.Internal,
			Message: err.Error(),
		}
	}

	returnTo, err := url.Parse(cfg.LogoutURL())
	if err != nil {
		return nil, &errs.Error{
			Code:    errs.Internal,
			Message: err.Error(),
		}
	}

	parameters := url.Values{}
	parameters.Add("returnTo", returnTo.String())
	parameters.Add("client_id", cfg.ClientID())
	logoutUrl.RawQuery = parameters.Encode()

	return &LogoutResponse{
		RedirectURL: logoutUrl.String(),
	}, nil
}

type ProfileData struct {
	Email   string `json:"email"`
	Picture string `json:"picture"`
}

type UserData struct {
	Auth0ID  string   `json:"auth0Id"`
	Email    string   `json:"email"`
	Roles    []string `json:"roles"`
	IsDriver bool     `json:"isDriver"`
}

//encore:authhandler
func AuthHandler(ctx context.Context, token string) (auth.UID, *UserData, error) {
	cleanToken := strings.TrimPrefix(token, "Bearer ")
	cleanToken = strings.TrimSpace(cleanToken)

	if cleanToken == "" {
		return "", nil, &errs.Error{Code: errs.Unauthenticated, Message: "missing auth token"}
	}

	parser := jwt.NewParser()
	parsedToken, _, err := parser.ParseUnverified(cleanToken, jwt.MapClaims{})
	if err != nil {
		return "", nil, &errs.Error{Code: errs.Unauthenticated, Message: "malformed jwt token"}
	}

	claims, ok := parsedToken.Claims.(jwt.MapClaims)
	if !ok {
		return "", nil, &errs.Error{Code: errs.Unauthenticated, Message: "invalid claims"}
	}

	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", nil, &errs.Error{Code: errs.Unauthenticated, Message: "missing sub claim"}
	}

	email, _ := claims["email"].(string)

	// Auth0 custom namespace claims (e.g. from your Action)
	var roles []string
	namespace := "https://staging-omw-backend-code-dwpi.encr.app"
	if rawRoles, exists := claims[namespace+"/roles"].([]interface{}); exists {
		for _, r := range rawRoles {
			if roleStr, ok := r.(string); ok {
				roles = append(roles, roleStr)
			}
		}
	}

	// Determine driver status directly
	isDriver := false
	for _, r := range roles {
		if strings.EqualFold(r, "driver") {
			isDriver = true
			break
		}
	}

	data := &UserData{
		Auth0ID:  sub,
		Email:    email,
		Roles:    roles,
		IsDriver: isDriver,
	}

	return auth.UID(sub), data, nil
}

// Endpoints annotated with `auth` are public and requires authentication
// Learn more: encore.dev/docs/primitives/apis#access-controls
//
//encore:api auth method=GET path=/profile
func GetProfile(ctx context.Context) (*ProfileData, error) {
	return auth.Data().(*ProfileData), nil
}
