package core

import (
	"net/http"

	"github.com/jmoiron/sqlx/types"
	"github.com/labstack/echo/v5"
)

// GetDashboardCharts returns chart data points to render on the dashboard.
func (c *Core) GetDashboardCharts() (types.JSONText, error) {
	out, err := c.st.Stats().DashboardCharts(bg)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "dashboard charts", "error", pqErrMsg(err)))
	}

	return types.JSONText(out), nil
}

// GetDashboardCounts returns stats counts to show on the dashboard.
func (c *Core) GetDashboardCounts() (types.JSONText, error) {
	out, err := c.st.Stats().DashboardCounts(bg)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "dashboard stats", "error", pqErrMsg(err)))
	}

	return types.JSONText(out), nil
}
