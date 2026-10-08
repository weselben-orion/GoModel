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

// breakerResetResponse is the body of a successful force reset. BreakersReset
// is 1 for the provider-level breaker plus every model-scoped breaker the
// provider instance had live at reset time.
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
	if err := resetter.ResetCircuitBreaker(name); err != nil {
		if errors.Is(err, providers.ErrProviderNotFound) {
			return handleError(c, core.NewNotFoundError("unknown provider: "+name))
		}
		return handleError(c, core.NewInvalidRequestErrorWithStatus(http.StatusConflict, err.Error(), err))
	}
	return c.JSON(http.StatusOK, breakerResetResponse{
		Provider: name,
		// The registry-backed resetter walks one provider instance; the count
		// distinguishes only reset-attempted (1) today — per-client counts
		// arrive with per-surface reporting if ever needed.
		BreakersReset: 1,
		ResetAt:       time.Now().UTC().Format(time.RFC3339),
	})
}
