package main

import (
	"context"

	"github.com/knadh/listmonk/internal/core"
	"github.com/knadh/listmonk/internal/manager"
	"github.com/knadh/listmonk/internal/media"
	lmstore "github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/models"
)

// store implements DataSource over the primary
// database.
type store struct {
	st    lmstore.Store
	core  *core.Core
	media media.Store
}

var bg = context.Background()

func newManagerStore(st lmstore.Store, c *core.Core, m media.Store) *store {
	return &store{
		st:    st,
		core:  c,
		media: m,
	}
}

// NextCampaigns retrieves active campaigns ready to be processed excluding
// campaigns that are also being processed. It also persists sent counts and
// resume checkpoints for campaigns being processed.
func (s *store) NextCampaigns(currentIDs []int64, sentCounts []int64, lastSubIDs []int64) ([]*models.Campaign, error) {
	progress := make([]lmstore.CampaignProgress, len(currentIDs))
	for i := range currentIDs {
		progress[i] = lmstore.CampaignProgress{ID: int(currentIDs[i]), Sent: int(sentCounts[i]), LastSubID: int(lastSubIDs[i])}
	}
	return s.st.Campaigns().Next(bg, progress)
}

// NextSubscribers retrieves a subset of subscribers of a given campaign.
// Since batches are processed sequentially, the retrieval is ordered by ID,
// and every batch takes the last ID of the last batch and fetches the next
// batch above that.
func (s *store) NextSubscribers(campID, lastFetchedID, limit int) ([]models.Subscriber, error) {
	return s.st.Campaigns().NextSubscribers(bg, campID, lastFetchedID, limit)
}

// GetCampaign fetches a campaign from the database.
func (s *store) GetCampaign(campID int) (*models.Campaign, error) {
	out, err := s.st.Campaigns().Get(bg, lmstore.CampaignRef{ID: campID}, lmstore.TemplateCampaign)
	return &out, err
}

// UpdateCampaignStatus updates a campaign's status.
func (s *store) UpdateCampaignStatus(campID int, status string) error {
	err := s.st.Campaigns().SetStatus(bg, campID, status)
	if err == lmstore.ErrNotFound {
		// upstream ignored a missing campaign here.
		return nil
	}
	return err
}

// UpdateCampaignCounts updates a campaign's status.
func (s *store) UpdateCampaignCounts(campID int, toSend int, sent int, lastSubID int) error {
	return s.st.Campaigns().SetCounts(bg, campID, toSend, sent, lastSubID)
}

// GetAttachment fetches a media attachment blob.
func (s *store) GetAttachment(mediaID int) (models.Attachment, error) {
	m, err := s.core.GetMedia(mediaID, "", "", s.media)
	if err != nil {
		return models.Attachment{}, err
	}

	b, err := s.media.GetBlob(m.URL)
	if err != nil {
		return models.Attachment{}, err
	}

	return models.Attachment{
		Name:    m.Filename,
		Content: b,
		Header:  manager.MakeAttachmentHeader(m.Filename, "base64", m.ContentType),
	}, nil
}

// GetInlineAttachmentByFilename fetches a media item by filename and returns
// it as an inline attachment along with the Content-ID value. The lookup is
// uniform across filesystem and S3 providers because both use the same media
// store interface; the first match for a given filename is returned.
func (s *store) GetInlineAttachmentByFilename(filename string) (models.Attachment, string, error) {
	m, err := s.core.GetMedia(0, "", filename, s.media)
	if err != nil {
		return models.Attachment{}, "", err
	}

	b, err := s.media.GetBlob(m.URL)
	if err != nil {
		return models.Attachment{}, "", err
	}

	cid := manager.MakeContentID(m.Filename)
	return models.Attachment{
		Name:     m.Filename,
		Content:  b,
		Header:   manager.MakeInlineAttachmentHeader(m.Filename, "", m.ContentType, cid),
		IsInline: true,
	}, cid, nil
}

// GetMediaURLByFilename returns the public URL of a media item by filename.
func (s *store) GetMediaURLByFilename(filename string) (string, error) {
	m, err := s.core.GetMedia(0, "", filename, s.media)
	if err != nil {
		return "", err
	}

	return m.URL, nil
}

// CreateLink registers a URL with a UUID for tracking clicks and returns the UUID.
func (s *store) CreateLink(url string) (string, error) {
	return s.st.Tracking().CreateLink(bg, url)
}

// BlocklistSubscriber blocklists a subscriber permanently.
func (s *store) BlocklistSubscriber(id int64) error {
	return s.st.Subscribers().Blocklist(bg, []int{int(id)})
}

// DeleteSubscriber deletes a subscriber from the DB.
func (s *store) DeleteSubscriber(id int64) error {
	return s.st.Subscribers().Delete(bg, []int{int(id)}, nil)
}
