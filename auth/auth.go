package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"encore.dev/beta/auth"
	"encore.dev/storage/sqldb"
	"google.golang.org/api/idtoken"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// 1. Define your service database tracking wrapper name matching your migration folder
// For example, if your migration folder is named 'auth', use that identifier here:
var authDB = sqldb.Named("auth")

// 2. Encore's automatic service initializer hook
func initService() (*Service, error) {
	// Wrap the native Encore raw SQL driver pool context directly inside GORM
	gormDB, err := gorm.Open(postgres.New(postgres.Config{
		Conn: authDB.Stdlib(),
	}), &gorm.Config{})

	if err != nil {
		return nil, fmt.Errorf("failed to connect database via GORM: %w", err)
	}

	// Returns the working instance so s.db is populated across all endpoints!
	return &Service{db: gormDB}, nil
}

// Replace this string literal with your real Google Client ID from the Google Developer Console
const googleClientID = "991675028668-c8ocrq0eggtrmdf3igspne342opcr4f2.apps.googleusercontent.com"

// The real implementation of the token verification
func verifyGoogleToken(ctx context.Context, idToken string) (*idtoken.Payload, error) {
	// Validate checks the signature, expiration, and ensures it was issued for your client ID
	payload, err := idtoken.Validate(ctx, idToken, googleClientID)
	if err != nil {
		return nil, fmt.Errorf("google token validation failed: %w", err)
	}

	return payload, nil
}

// Add a specific request body structure for our raw JSON receiver
type GoogleLoginRequest struct {
	IDToken string `json:"idToken"`
}

//encore:service
type Service struct {
	db *gorm.DB
}

// AuthData holds our session metadata stored in the request context
type AuthData struct {
	Email string
}

// AuthParams instructs Encore's gateway to automatically extract cookies
type AuthParams struct {
	SessionCookie *http.Cookie `cookie:"session"`
}

// //encore:authhandler
// func EncoreAuthHandler(ctx context.Context, params *AuthParams) (auth.UID, *AuthData, error) {
// 	fmt.Printf("AuthHandler triggered. Token/Cookie value: %s\n", params.SessionCookie)
// 	if params.SessionCookie == nil || params.SessionCookie.Value == "" {
// 		return "", nil, &errs.Error{
// 			Code:    errs.Unauthenticated,
// 			Message: "Missing session cookie.",
// 		}
// 	}

// 	// In production, look up params.SessionCookie.Value in your database here.
// 	// For this scratch implementation, we will simulate a successful match:
// 	userID := "usr_12345"
// 	userEmail := "developer@example.com"

// 	return auth.UID(userID), &AuthData{Email: userEmail}, nil
// }

// //encore:				authhandler
func (s *Service) AuthHandler(ctx context.Context, token string) (auth.UID, *User, error) {
	// 1. Sanitize the incoming authorization header string
	// If you sent it as "Bearer <token>", clean it up so we just have the raw hex string
	// rawToken := strings.TrimPrefix(token, "Bearer ")
	// rawToken = strings.TrimSpace(rawToken)

	// if rawToken == "" {
	// 	return "", nil, errors.New("missing or malformed authorization token")
	// }

	// // 2. Look up the token in your fresh database session table
	// var session Session
	// err := s.db.Where("token = ?", rawToken).First(&session).Error
	// if err != nil {
	// 	if errors.Is(err, gorm.ErrRecordNotFound) {
	// 		return "", nil, errors.New("invalid or expired session")
	// 	}
	// 	return "", nil, err
	// }

	// // 3. Locate the corresponding User profile row
	// var user User
	// err = s.db.Where("id = ?", session.UserID).First(&user).Error
	// if err != nil {
	// 	return "", nil, errors.New("user associated with session not found")
	// }

	// // 4. Return the unique ID string and the authenticated context payload object
	// // Convert your numeric database primary key to a string for Encore's UID type
	// return auth.UID(strconv.FormatUint(uint64(user.ID), 10)), &user, nil
	return "", nil, nil
}

type LoginParams struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Create a struct to map the columns we care about
type User struct {
	PasswordHash string `gorm:"not null"`
	ID           int    `gorm:"column:id;primaryKey;autoIncrement"`
	Email        string `gorm:"column:email;uniqueIndex;not null"`
	Name         string `gorm:"column:name;not null"`
}

// GoogleLoginParams captures the JSON payload sent by the frontend
type GoogleLoginParams struct {
	IDToken string `json:"idToken"`
}

// LoginResponse defines the shape of the token token returned to the frontend
type LoginResponse struct {
	Token string `json:"token"`
}

// Session maps your internal backend authorization tokens to database rows
type Session struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement"`
	UserID    int       `gorm:"column:user_id;not null"`
	Token     string    `gorm:"column:token;uniqueIndex;not null"`
	CreatedAt time.Time `gorm:"column:created_at;default:now()"`
}

// TableName explicitly binds this struct to your Postgres sessions table
func (Session) TableName() string {
	return "user_sessions"
}

//encore:api public path=/auth/google method=POST
func (s *Service) LoginWithGoogle(ctx context.Context, params *GoogleLoginParams) (*LoginResponse, error) {
	// 1. Verify the idToken safely
	payload, err := verifyGoogleToken(ctx, params.IDToken)

	// ✅ CRUCIAL FIX: If validation fails or payload is empty, stop immediately.
	// This prevents the nil pointer panic on the next lines.
	if err != nil || payload == nil {
		return nil, fmt.Errorf("google token verification failed: %w", err)
	}

	// 2. Safely extract verified info from the Claims map now that we know payload is real
	email, _ := payload.Claims["email"].(string)
	name, _ := payload.Claims["name"].(string)

	// 3. Sync or locate the user in your PostgreSQL database
	var user User
	// We pass PasswordHash here to satisfy your database's NOT NULL constraint!
	err = s.db.Where(User{Email: email}).Attrs(User{
		Name:         name,
		Email:        email,
		PasswordHash: "OAUTH_EXTERNAL_USER",
	}).FirstOrCreate(&user).Error

	if err != nil {
		return nil, fmt.Errorf("database tracking failed: %w", err)
	}

	// 4. Generate your internal backend session token string
	sessionToken := generateSecureToken()

	// Save this tracking session token into your database session table
	err = s.db.Create(&Session{UserID: int(user.ID), Token: sessionToken}).Error
	if err != nil {
		return nil, fmt.Errorf("failed to persist active session: %w", err)
	}

	// Return the clean token to Angular
	return &LoginResponse{Token: sessionToken}, nil
}

//encore:api public raw method=POST path=/auth/login
func (s *Service) Login(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// // 1. Parse the incoming multi-part form data
	// err := req.ParseMultipartForm(32 << 20) // max memory 32MB
	// if err != nil {
	// 	w.WriteHeader(http.StatusBadRequest)
	// 	w.Write([]byte(`{"message": "Invalid form layout data"}`))
	// 	return
	// }

	// // 2. Extract fields directly using FormValue
	// email := req.FormValue("username")
	// password := req.FormValue("password")

	// // Safety check: Ensure database pointer isn't nil
	// if s.db == nil {
	// 	w.WriteHeader(http.StatusInternalServerError)
	// 	w.Write([]byte(`{"message": "Database connection is uninitialized"}`))
	// 	return
	// }

	// // 3. Query the database using GORM syntax
	// var user User
	// err = s.db.WithContext(req.Context()).Where("email = ?", email).First(&user).Error

	// if err != nil {
	// 	if errors.Is(err, gorm.ErrRecordNotFound) {
	// 		w.WriteHeader(http.StatusUnauthorized)
	// 		w.Write([]byte(`{"message": "Invalid email or password"}`))
	// 		return
	// 	}

	// 	// ✅ FIX: Instead of a blind 500, return the exact DB error string to see what failed
	// 	w.WriteHeader(http.StatusInternalServerError)
	// w.Write([]byte(`{"message": "Database lookup failed", "error": "` + err.Error() + `"}`))
	// 	return
	// }

	// 4. Compare the plain-text password with the stored secure hash
	// err = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password))
	// if err != nil {
	// 	w.WriteHeader(http.StatusUnauthorized)
	// 	w.Write([]byte(`{"message": "Invalid email or password"}`))
	// 	return
	// }

	// 5. Issue the secure tracking session token
	sessionToken := generateSecureToken()

	// // ✅ CRUCIAL FIX: Persist the session to user_sessions table so your AuthHandler can find it!
	// err = s.db.WithContext(req.Context()).Create(&Session{
	// 	UserID: int(user.ID),
	// 	Token:  sessionToken,
	// }).Error
	// if err != nil {
	// 	w.WriteHeader(http.StatusInternalServerError)
	// 	w.Write([]byte(`{"message": "Failed to save session to database", "error": "` + err.Error() + `"}`))
	// 	return
	// }

	// 6. Set the fallback cookie for browser compatibility
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   false, // Keep false for local localhost HTTP testing
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(7 * 24 * time.Hour),
	})

	// ✅ Return the token explicitly in the JSON response body so your Angular app can capture it
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status": "success", "token": "` + sessionToken + `"}`))
}

//encore:api public raw method=POST path=/auth/logout
func Logout(w http.ResponseWriter, req *http.Request) {
	// Clear the cookie out of the browser instantly by passing a negative MaxAge
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"logged_out"}`))
}

func generateSecureToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// Define a secret key for signing tokens.
// In production, keep this in an Encore secret configuration!
var jwtSecretKey = []byte("your-super-secret-dev-key-change-in-prod")

// Custom Claims layout that will be baked into the token string
type CustomClaims struct {
	UserID uint64 `json:"user_id"`
	Email  string `json:"email"`
	// jwt.RegisteredClaims
}

type RefreshToken struct {
	ID        uint64    `gorm:"primaryKey"`
	UserID    uint64    `gorm:"column:user_id;not null"`
	Token     string    `gorm:"column:token;uniqueIndex;not null"`
	ExpiresAt time.Time `gorm:"column:expires_at;not null"`
}

func (RefreshToken) TableName() string {
	return "user_refresh_tokens"
}

type RefreshParams struct {
	RefreshToken string `json:"refresh_token"`
}

type RefreshResponse struct {
	AccessToken string `json:"access_token"`
}

// //encore:api public raw method=POST path=/auth/login
// func (s *Service) Logins(w http.ResponseWriter, req *http.Request) {
// 	w.Header().Set("Content-Type", "application/json")

// 	// ... [Keep your form parsing, DB lookup, and bcrypt checks exactly the same] ...

// 	// 1. Generate the Short-Lived Access Token Claims (Stateless - Valid for 15 Mins)
// 	accessClaims := CustomClaims{
// 		UserID: user.ID,
// 		Email:  user.Email,
// 		RegisteredClaims: jwt.RegisteredClaims{
// 			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
// 			IssuedAt:  jwt.NewNumericDate(time.Now()),
// 			Issuer:    "my-encore-app",
// 		},
// 	}
// 	accessToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString(jwtSecretKey)
// 	if err != nil {
// 		w.WriteHeader(http.StatusInternalServerError)
// 		return
// 	}

// 	// 2. Generate a secure random Refresh Token (Stateful - Valid for 7 Days)
// 	refreshStr := generateSecureToken() // Your hex generator function
// 	refreshExpiry := time.Now().Add(7 * 24 * time.Hour)

// 	// Persist it so we have the absolute power to revoke it later
// 	err = s.db.WithContext(req.Context()).Create(&RefreshToken{
// 		UserID:    user.ID,
// 		Token:     refreshStr,
// 		ExpiresAt: refreshExpiry,
// 	}).Error
// 	if err != nil {
// 		w.WriteHeader(http.StatusInternalServerError)
// 		return
// 	}

// 	// 3. Return both to Angular
// 	w.WriteHeader(http.StatusOK)
// 	w.Write([]byte(`{
// 		"status": "success",
// 		"access_token": "` + accessToken + `",
// 		"refresh_token": "` + refreshStr + `"
// 	}`))
// }
// //encore:api public path=/auth/refresh method=POST
// func (s *Service) RefreshSession(ctx context.Context, params *RefreshParams) (*RefreshResponse, error) {
// 	var storedToken RefreshToken

// 	// 1. Check if the refresh token exists in our DB
// 	err := s.db.WithContext(ctx).Where("token = ?", params.RefreshToken).First(&storedToken).Error
// 	if err != nil {
// 		return nil, &encore.APIError{Code: encore.Unauthenticated, Message: "Invalid refresh token"}
// 	}

// 	// 2. Verify it hasn't expired
// 	if time.Now().After(storedToken.ExpiresAt) {
// 		s.db.WithContext(ctx).Delete(&storedToken) // Clean up expired token row
// 		return nil, &encore.APIError{Code: encore.Unauthenticated, Message: "Refresh token expired"}
// 	}

// 	// 3. Look up the corresponding user profile data
// 	var user User
// 	if err := s.db.WithContext(ctx).First(&user, storedToken.UserID).Error; err != nil {
// 		return nil, &encore.APIError{Code: encore.NotFound, Message: "User not found"}
// 	}

// 	// 4. Issue a brand new, stateless access token for another 15 minutes
// 	accessClaims := CustomClaims{
// 		UserID: user.ID,
// 		Email:  user.Email,
// 		RegisteredClaims: jwt.RegisteredClaims{
// 			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
// 			IssuedAt:  jwt.NewNumericDate(time.Now()),
// 			Issuer:    "my-encore-app",
// 		},
// 	}
// 	newAccessToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString(jwtSecretKey)
// 	if err != nil {
// 		return nil, &encore.APIError{Code: encore.Internal, Message: "Token generation failed"}
// 	}

// 	return &RefreshResponse{AccessToken: newAccessToken}, nil
// }
