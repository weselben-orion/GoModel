package admin

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// breakerResetResponse is the body of a successful force reset.
// BreakersReset counts provider instances reset (0 or 1), not individual
// breakers: one provider instance may carry a provider-level breaker plus
// any number of model-scoped breakers, and all of them were reset.
type breakerResetResponse struct {
	Provider      string `json:"provider"`
	BreakersReset int    `json:"breakers_reset"`
	ResetAt       string `json:"reset_at"`
}

// ResetProviderBreaker handles POST /admin/providers/:name/breaker/reset.
// It force-closes the named provider's circuit breaker(s) — the provider-level
// breaker and every model-scoped breaker of that instance — so traffic resumes
// immediately instead of waiting out the open-state timeout. A provider whose
// breaker is disabled is a successful no-op.
func (h *Handler) ResetProviderBreaker(c *echo.Context) error {
	if h.registry == nil {
		return handleError(c, featureUnavailableError("provider registry is unavailable"))
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		return handleError(c, core.NewNotFoundError("provider name is required"))
	}
	resetter := providers.NewBreakerResetter(h.registry)
	count, err := resetter.ResetCircuitBreaker(name)
	if err != nil {
		if errors.Is(err, providers.ErrProviderNotFound) {
			return handleError(c, core.NewNotFoundError("unknown provider: "+name))
		}
		// Every other error the registry-backed resetter returns today is the
		// unsupported-provider sentinel; map it to 409 so the dashboard can
		// tell "nothing to reset" apart from "provider missing".
		return handleError(c, core.NewInvalidRequestErrorWithStatus(http.StatusConflict, err.Error(), err))
	}
	return c.JSON(http.StatusOK, breakerResetResponse{
		Provider:      name,
		BreakersReset: count,
		ResetAt:       time.Now().UTC().Format(time.RFC3339),
	})
}
