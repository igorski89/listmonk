package core

import (
	"errors"
	"net/http"

	"github.com/knadh/listmonk/internal/media"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/labstack/echo/v5"
	"gopkg.in/volatiletech/null.v6"
)

// QueryMedia returns media entries optionally filtered by a query string.
func (c *Core) QueryMedia(provider string, s media.Store, query string, tags []string, offset, limit int) ([]media.Media, int, error) {
	out, total, err := c.st.Media().Query(bg, provider, query, tags, store.Page{Offset: offset, Limit: limit})
	if err != nil {
		return out, 0, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching",
				"name", "{globals.terms.media}", "error", pqErrMsg(err)))
	}

	for i := 0; i < len(out); i++ {
		out[i].URL = s.GetURL(out[i].Filename)
		if out[i].Thumb != "" {
			out[i].ThumbURL = null.String{Valid: true, String: s.GetURL(out[i].Thumb)}
		}
	}

	return out, total, nil
}

// GetMedia returns a media item.
func (c *Core) GetMedia(id int, uuid, fileName string, s media.Store) (media.Media, error) {
	out, err := c.st.Media().Get(bg, store.MediaRef{ID: id, UUID: uuid, Filename: fileName})
	if err != nil {
		// If it's ` sql: no rows in result set`, return a 404.
		if errors.Is(err, store.ErrNotFound) {
			return out, ErrNotFound
		}

		return out, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.media}", "error", pqErrMsg(err)))
	}

	out.URL = s.GetURL(out.Filename)
	if out.Thumb != "" {
		out.ThumbURL = null.String{Valid: true, String: s.GetURL(out.Thumb)}
	}

	return out, nil
}

// GetMediaTags returns tags for the given provider.
func (c *Core) GetMediaTags(provider string) ([]media.Tag, error) {
	out, err := c.st.Media().Tags(bg, provider)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.terms.tags}", "error", pqErrMsg(err)))
	}

	return out, nil
}

// InsertMedia inserts a new media file into the DB.
func (c *Core) InsertMedia(fileName, thumbName, contentType string, meta models.JSON, tags []string, provider string, s media.Store) (media.Media, error) {
	out, err := c.st.Media().Insert(bg, store.MediaInput{Filename: fileName, Thumb: thumbName, ContentType: contentType,
		Provider: provider, Meta: meta, Tags: normalizeTags(tags)})
	if err != nil {
		c.log.Printf("error inserting uploaded file to db: %v", err)
		return media.Media{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorCreating", "name", "{globals.terms.media}", "error", pqErrMsg(err)))
	}

	return c.GetMedia(out.ID, "", "", s)
}

// UpdateMedia updates a media item's tags.
func (c *Core) UpdateMedia(id int, tags []string, s media.Store) (media.Media, error) {
	if _, err := c.st.Media().SetTags(bg, id, normalizeTags(tags)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return media.Media{}, ErrNotFound
		}
		return media.Media{}, echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorUpdating", "name", "{globals.terms.media}", "error", pqErrMsg(err)))
	}

	return c.GetMedia(id, "", "", s)
}

// DeleteMedia deletes a given media item and returns the filename of the deleted item.
func (c *Core) DeleteMedia(id int) (string, error) {
	fname, err := c.st.Media().Delete(bg, id)
	if err != nil {
		c.log.Printf("error inserting uploaded file to db: %v", err)
		return "", echo.NewHTTPError(http.StatusInternalServerError,
			c.i18n.Ts("globals.messages.errorCreating", "name", "{globals.terms.media}", "error", pqErrMsg(err)))
	}

	return fname, nil
}
