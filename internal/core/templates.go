package core

import (
	"errors"
	"net/http"

	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/labstack/echo/v5"
	null "gopkg.in/volatiletech/null.v6"
)

// GetTemplates retrieves templates filtered by name and type.
func (c *Core) GetTemplates(searchStr, typ string, noBody bool, orderBy, order string) ([]models.Template, error) {
	if order == "" {
		order = models.OrderAsc
	}

	out, err := c.st.Templates().Query(bg, makeSearchString(searchStr), typ, noBody, sortOf(orderBy, order, templateQuerySortFields))
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.templates}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// GetTemplate retrieves a given template.
func (c *Core) GetTemplate(id int, noBody bool) (models.Template, error) {
	out, err := c.st.Templates().Get(bg, id, noBody)
	if errors.Is(err, store.ErrNotFound) {
		return models.Template{}, echo.NewHTTPError(http.StatusBadRequest,
			c.i18n.Ts("globals.messages.notFound", "name", "{globals.terms.template}"))
	}
	if err != nil {
		return models.Template{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.templates}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// CreateTemplate creates a new template.
func (c *Core) CreateTemplate(name, typ, subject string, body []byte, bodySource null.String) (models.Template, error) {
	out, err := c.st.Templates().Create(bg, store.TemplateInput{Name: name, Type: typ, Subject: subject, Body: string(body), BodySource: bodySource})
	if err != nil {
		return models.Template{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorCreating", "name", "{globals.terms.template}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// UpdateTemplate updates a given template.
func (c *Core) UpdateTemplate(id int, name, subject string, body []byte, bodySource null.String) (models.Template, error) {
	out, err := c.st.Templates().Update(bg, id, store.TemplateInput{Name: name, Subject: subject, Body: string(body), BodySource: bodySource})
	if errors.Is(err, store.ErrNotFound) {
		return models.Template{}, echo.NewHTTPError(http.StatusBadRequest,
			c.i18n.Ts("globals.messages.notFound", "name", "{globals.terms.template}"))
	}
	if err != nil {
		return models.Template{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.template}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// SetDefaultTemplate sets a template as default.
func (c *Core) SetDefaultTemplate(id int) error {
	if err := c.st.Templates().SetDefault(bg, id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.template}", "error", pqErrMsg(err)))
	}

	return nil
}

// DeleteTemplate deletes a given template.
func (c *Core) DeleteTemplate(id int) error {
	err := c.st.Templates().Delete(bg, id)
	if errors.Is(err, store.ErrNotFound) {
		return echo.NewHTTPError(http.StatusBadRequest, c.i18n.T("templates.cantDeleteDefault"))
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorDeleting", "name", "{globals.terms.template}", "error", pqErrMsg(err)))
	}

	return nil
}
