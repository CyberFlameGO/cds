package api

import (
	"context"
	"net/http"
	"time"

	jwt "github.com/golang-jwt/jwt"
	"github.com/gorilla/mux"
	"github.com/rockbears/log"

	"github.com/ovh/cds/engine/api/authentication"
	"github.com/ovh/cds/engine/api/services"
	"github.com/ovh/cds/engine/api/user"
	"github.com/ovh/cds/engine/api/worker"
	"github.com/ovh/cds/engine/service"
	"github.com/ovh/cds/sdk"
	cdslog "github.com/ovh/cds/sdk/log"
	"github.com/ovh/cds/sdk/telemetry"
)

const (
	xsrfHeaderName = "X-XSRF-TOKEN"
	xsrfCookieName = "xsrf_token"
)

func (api *API) jwtMiddleware(ctx context.Context, w http.ResponseWriter, req *http.Request, rc *service.HandlerConfig) (context.Context, error) {
	ctx, end := telemetry.Span(ctx, "router.jwtMiddleware")
	defer end()

	return service.JWTMiddleware(ctx, w, req, rc, authentication.VerifyJWT)
}

func (api *API) authAdminMiddleware(ctx context.Context, w http.ResponseWriter, req *http.Request, rc *service.HandlerConfig) (context.Context, error) {
	ctx, end := telemetry.Span(ctx, "router.authAdminMiddleware")
	defer end()

	ctx, err := api.authMiddleware(ctx, w, req, rc)
	if err != nil {
		return ctx, err
	}

	// Exclude consumers not admin or admin that are used for services
	if !isAdmin(ctx) || isService(ctx) {
		return ctx, sdk.WithStack(sdk.ErrForbidden)
	}

	trackSudo(ctx, w)

	return ctx, nil
}

func (api *API) authMaintainerMiddleware(ctx context.Context, w http.ResponseWriter, req *http.Request, rc *service.HandlerConfig) (context.Context, error) {
	ctx, end := telemetry.Span(ctx, "router.authMaintainerMiddleware")
	defer end()

	ctx, err := api.authMiddleware(ctx, w, req, rc)
	if err != nil {
		return ctx, err
	}

	// Excluse consumers not maintainer or admin that are used for services
	if !isMaintainer(ctx) || isService(ctx) {
		return ctx, sdk.WithStack(sdk.ErrForbidden)
	}

	return ctx, nil
}

func (api *API) authMiddleware(ctx context.Context, w http.ResponseWriter, req *http.Request, rc *service.HandlerConfig) (context.Context, error) {
	ctx, end := telemetry.Span(ctx, "router.authMiddleware")
	defer end()

	ctx, err := api.authOptionalMiddleware(ctx, w, req, rc)
	if err != nil {
		return ctx, err
	}

	// We should have a consumer in the context to validate the auth
	var apiConsumer = getAPIConsumer(ctx)
	if apiConsumer == nil {
		return ctx, sdk.WithStack(sdk.ErrUnauthorized)
	}

	return ctx, nil
}

func (api *API) authOptionalMiddleware(ctx context.Context, w http.ResponseWriter, req *http.Request, rc *service.HandlerConfig) (context.Context, error) {
	ctx, end := telemetry.Span(ctx, "router.authOptionalMiddleware")
	defer end()

	// Check for a JWT in current request and add it to the context
	// If a JWT is given, we also checks that there are a valid session and consumer for it
	jwt, ok := ctx.Value(service.ContextJWT).(*jwt.Token)
	if !ok {
		log.Debug(ctx, "api.authOptionalMiddleware> no jwt token found in context")
		return ctx, nil
	}
	claims := jwt.Claims.(*sdk.AuthSessionJWTClaims)
	ctx = context.WithValue(ctx, cdslog.AuthSessionTokenID, claims.TokenID)
	SetTracker(w, cdslog.AuthSessionTokenID, claims.TokenID)
	ctx = context.WithValue(ctx, contextClaims, claims)

	// Check for session based on jwt from context
	sessionID := claims.StandardClaims.Id
	session, err := authentication.CheckSession(ctx, api.mustDB(), api.Cache, sessionID)
	if err != nil {
		log.Warn(ctx, "authMiddleware> cannot find a valid session for given JWT: %v", err)
	}
	if session == nil {
		log.Debug(ctx, "api.authOptionalMiddleware> no session found in context")
		return ctx, nil
	}
	ctx = context.WithValue(ctx, cdslog.AuthSessionID, session.ID)
	SetTracker(w, cdslog.AuthSessionID, session.ID)
	ctx = context.WithValue(ctx, cdslog.AuthSessionIAT, session.Created.Unix())
	SetTracker(w, cdslog.AuthSessionIAT, session.Created.Unix())
	ctx = context.WithValue(ctx, contextSession, session)

	// Load auth consumer for current session in database with authentified user and contacts
	consumer, err := authentication.LoadConsumerByID(ctx, api.mustDB(), session.ConsumerID,
		authentication.LoadConsumerOptions.WithAuthentifiedUser)
	if err != nil {
		return ctx, sdk.NewErrorWithStack(err, sdk.ErrUnauthorized)
	}
	ctx = context.WithValue(ctx, cdslog.AuthUserID, consumer.AuthentifiedUserID)
	SetTracker(w, cdslog.AuthUserID, consumer.AuthentifiedUserID)
	ctx = context.WithValue(ctx, cdslog.AuthConsumerID, consumer.ID)
	SetTracker(w, cdslog.AuthConsumerID, consumer.ID)

	// If the consumer is disabled, return an error
	if consumer.Disabled {
		return ctx, sdk.WrapError(sdk.ErrUnauthorized, "consumer (%s) is disabled", consumer.ID)
	}

	// If the driver was disabled for the consumer that was found, ignore it
	var driverManifest *sdk.AuthDriverManifest
	if authDriver, ok := api.AuthenticationDrivers[consumer.Type]; ok {
		m := authDriver.GetManifest()
		driverManifest = &m
	}
	if driverManifest == nil {
		return ctx, sdk.WrapError(sdk.ErrUnauthorized, "consumer driver (%s) was not found", consumer.Type)
	}
	ctx = context.WithValue(ctx, contextDriverManifest, driverManifest)

	// Add contacts for consumer's user
	if err := user.LoadOptions.WithContacts(ctx, api.mustDB(), consumer.AuthentifiedUser); err != nil {
		return ctx, err
	}

	// Add service for consumer if exists
	consumer.Service, err = services.LoadByConsumerID(ctx, api.mustDB(), consumer.ID)
	if err != nil && !sdk.ErrorIs(err, sdk.ErrNotFound) {
		return ctx, err
	}
	if consumer.Service != nil {
		ctx = context.WithValue(ctx, cdslog.AuthServiceName, consumer.Service.Name)
		SetTracker(w, cdslog.AuthServiceName, consumer.Service.Name)
	}

	// Add worker for consumer if exists
	consumer.Worker, err = worker.LoadByConsumerID(ctx, api.mustDB(), consumer.ID)
	if err != nil && !sdk.ErrorIs(err, sdk.ErrNotFound) {
		return ctx, err
	}
	if consumer.Worker != nil {
		ctx = context.WithValue(ctx, cdslog.AuthWorkerName, consumer.Worker.Name)
		SetTracker(w, cdslog.AuthWorkerName, consumer.Worker.Name)
	}

	if consumer.Service == nil && consumer.Worker == nil {
		ctx = context.WithValue(ctx, cdslog.AuthUsername, consumer.AuthentifiedUser.Username)
		SetTracker(w, cdslog.AuthUsername, consumer.AuthentifiedUser.Username)
	}

	ctx = context.WithValue(ctx, contextConsumer, consumer)

	// Checks scopes, one of expected scopes should be in actual scopes
	// Actual scope empty list means wildcard scope, we don't need to check scopes
	expectedScopes, actualScopes := rc.AllowedScopes, consumer.ScopeDetails
	if len(expectedScopes) > 0 && len(actualScopes) > 0 {
		var found bool
	findScope:
		for i := range expectedScopes {
			for j := range actualScopes {
				if actualScopes[j].Scope == expectedScopes[i] {
					// Check if there are scope details, if yes we should check if current route/method is allowed in restrictions
					if len(actualScopes[j].Endpoints) == 0 {
						found = true
						break findScope
					}

					// if the route is not in current consumer allowed endpoints we should not validate the scope
					if exists, endpoint := actualScopes[j].Endpoints.FindEndpoint(rc.CleanURL); exists &&
						len(endpoint.Methods) == 0 || endpoint.Methods.Contains(rc.Method) {
						found = true
						break findScope
					}
				}
			}
		}
		if !found {
			return ctx, sdk.WrapError(sdk.ErrUnauthorized, "token scopes doesn't match expected: %v", expectedScopes)
		}
	}

	// Check that permission are valid for current route and consumer
	if err := api.checkPermission(ctx, w, mux.Vars(req), rc.PermissionLevel); err != nil {
		return ctx, err
	}

	return ctx, nil
}

func (api *API) xsrfMiddleware(ctx context.Context, w http.ResponseWriter, req *http.Request, rc *service.HandlerConfig) (context.Context, error) {
	ctx, end := telemetry.Span(ctx, "router.xsrfMiddleware")
	defer end()

	// If no consumer in the context, means that the route is not authentified, we don't need to check the xsrf token.
	if getAPIConsumer(ctx) == nil {
		return ctx, nil
	}

	jwtFromCookieVal := ctx.Value(service.ContextJWTFromCookie)
	jwtFromCookie, _ := jwtFromCookieVal.(bool)
	if !jwtFromCookie {
		return ctx, nil
	}

	session := getAuthSession(ctx)

	xsrfToken := req.Header.Get(xsrfHeaderName)
	existingXSRFToken, existXSRFTokenInCache := authentication.GetSessionXSRFToken(api.Cache, session.ID)

	xsrfTokenCookie, _ := req.Cookie(xsrfCookieName)
	xsrfTokenCookieExistInCookie := xsrfTokenCookie != nil

	// If it's not a read request we want to check the xsrf token then generate a new one
	// else if its a read request we want to reuse a cached XSRF token or generate one if not in cache or nothing given by the client
	if rc.PermissionLevel > sdk.PermissionRead {
		if !existXSRFTokenInCache || xsrfToken != existingXSRFToken {
			// We want to return a forbidden to allow the user to retry with a new token.
			return ctx, sdk.WithStack(sdk.ErrForbidden)
		}
	} else {
		if !existXSRFTokenInCache || !xsrfTokenCookieExistInCookie {
			sessionSecondsBeforeExpiration := int(session.ExpireAt.Sub(time.Now()).Seconds())
			var err error
			existingXSRFToken, err = authentication.NewSessionXSRFToken(api.Cache, session.ID, sessionSecondsBeforeExpiration)
			if err != nil {
				return ctx, err
			}
		}

		// Set a cookie with the jwt token
		api.SetCookieSession(w, xsrfCookieName, existingXSRFToken)
	}

	return ctx, nil
}
