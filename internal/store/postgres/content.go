package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
	"github.com/jmoiron/sqlx/types"
	"github.com/knadh/listmonk/internal/media"
	"github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
	"github.com/lib/pq"
)

type templates struct{ *Store }
type mediaStore struct{ *Store }
type settings struct{ *Store }

// Templates.

func (s templates) Query(ctx context.Context, search, typ string, noBody bool, srt store.Sort) ([]models.Template, error) {
	out := []models.Template{}
	stmt := replaceOrder(s.q.GetTemplates, srt)
	if err := s.db.SelectContext(ctx, &out, stmt, 0, noBody, typ, search); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func (s templates) Get(ctx context.Context, id int, noBody bool) (models.Template, error) {
	var out []models.Template
	stmt := replaceOrder(s.q.GetTemplates, store.Sort{Field: models.FieldCreatedAt})
	if err := s.db.SelectContext(ctx, &out, stmt, id, noBody, "", ""); err != nil {
		return models.Template{}, wrap(err)
	}
	if len(out) == 0 {
		return models.Template{}, store.ErrNotFound
	}
	return out[0], nil
}

func (s templates) Create(ctx context.Context, t store.TemplateInput) (models.Template, error) {
	var newID int
	if err := s.q.CreateTemplate.GetContext(ctx, &newID, t.Name, t.Type, t.Subject, []byte(t.Body), t.BodySource); err != nil {
		return models.Template{}, wrap(err)
	}
	return s.Get(ctx, newID, false)
}

func (s templates) Update(ctx context.Context, id int, t store.TemplateInput) (models.Template, error) {
	res, err := s.q.UpdateTemplate.ExecContext(ctx, id, t.Name, t.Subject, []byte(t.Body), t.BodySource)
	if err != nil {
		return models.Template{}, wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return models.Template{}, store.ErrNotFound
	}
	return s.Get(ctx, id, false)
}

func (s templates) SetDefault(ctx context.Context, id int) error {
	_, err := s.q.SetDefaultTemplate.ExecContext(ctx, id)
	return wrap(err)
}

func (s templates) Delete(ctx context.Context, id int) error {
	var delID int
	if err := s.q.DeleteTemplate.GetContext(ctx, &delID, id); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return wrap(err)
	}
	if delID == 0 {
		return store.ErrNotFound
	}
	return nil
}

// Media.

func (s mediaStore) Query(ctx context.Context, provider, search string, tags []string, p store.Page) ([]media.Media, int, error) {
	out := []media.Media{}
	if search != "" {
		search = strings.ToLower(search)
	}
	if err := s.q.QueryMedia.SelectContext(ctx, &out, fmt.Sprintf("%%%s%%", search), provider, p.Offset, p.Limit, pq.StringArray(strs(tags))); err != nil {
		return out, 0, wrap(err)
	}
	total := 0
	if len(out) > 0 {
		total = out[0].Total
	}
	return out, total, nil
}

func (s mediaStore) Get(ctx context.Context, ref store.MediaRef) (media.Media, error) {
	var out media.Media
	if err := s.q.GetMedia.GetContext(ctx, &out, ref.ID, uuidArg(ref.UUID), ref.Filename); err != nil {
		return out, notFound(err)
	}
	return out, nil
}

func (s mediaStore) Tags(ctx context.Context, provider string) ([]media.Tag, error) {
	out := []media.Tag{}
	if err := s.q.GetMediaTags.SelectContext(ctx, &out, provider); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func (s mediaStore) Insert(ctx context.Context, m store.MediaInput) (media.Media, error) {
	uu, err := uuid.NewV4()
	if err != nil {
		return media.Media{}, err
	}
	var newID int
	if err := s.q.InsertMedia.GetContext(ctx, &newID, uu, m.Filename, m.Thumb, m.ContentType, m.Provider, m.Meta, pq.StringArray(m.Tags)); err != nil {
		return media.Media{}, wrap(err)
	}
	return s.Get(ctx, store.MediaRef{ID: newID})
}

func (s mediaStore) SetTags(ctx context.Context, id int, tags []string) (media.Media, error) {
	res, err := s.q.UpdateMedia.ExecContext(ctx, id, pq.StringArray(tags))
	if err != nil {
		return media.Media{}, wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return media.Media{}, store.ErrNotFound
	}
	return s.Get(ctx, store.MediaRef{ID: id})
}

func (s mediaStore) Delete(ctx context.Context, id int) (string, error) {
	var fname string
	if err := s.q.DeleteMedia.GetContext(ctx, &fname, id); err != nil {
		return "", wrap(err)
	}
	return fname, nil
}

// Settings. All() works before Configure (it's read to build Options).

func (s settings) All(ctx context.Context) (json.RawMessage, error) {
	var b types.JSONText
	if err := s.db.GetContext(ctx, &b, s.qMap["get-settings"].Query); err != nil {
		return nil, wrap(err)
	}
	return json.RawMessage(b), nil
}

func (s settings) Update(ctx context.Context, v json.RawMessage) error {
	_, err := s.q.UpdateSettings.ExecContext(ctx, []byte(v))
	return wrap(err)
}

func (s settings) Set(ctx context.Context, key string, value json.RawMessage) error {
	_, err := s.q.UpdateSettingsByKey.ExecContext(ctx, key, value)
	return wrap(err)
}
