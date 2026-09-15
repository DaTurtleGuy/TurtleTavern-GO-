package handlers

import (
	"context"
	"net/http"

	"github.com/TurtleTavern/turtletavern/internal/models"
)

func getUserCtx(r *http.Request) *models.UserContext {
	v := r.Context().Value(models.UserContextKey{})
	if v == nil {
		return nil
	}
	uc, ok := v.(*models.UserContext)
	if !ok {
		return nil
	}
	return uc
}

func withUserCtx(ctx context.Context, uc *models.UserContext) context.Context {
	return context.WithValue(ctx, models.UserContextKey{}, uc)
}
