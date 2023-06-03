package internal

import (
	"fmt"

	"github.com/hbomb79/Thea/internal/database"
	"github.com/hbomb79/Thea/internal/export"
	"github.com/hbomb79/Thea/internal/ffmpeg"
	"github.com/hbomb79/Thea/internal/queue"
	"github.com/hbomb79/Thea/pkg/logger"
)

// QueueService is responsible for exposing methods for reading or mutating
// the state of the Thea queue.
type QueueService interface {
	GetAllItems() *[]*queue.Item
	GetItem(int) (*queue.Item, error)
	ReorderQueue([]int) error
	PromoteItem(int) error
	CancelItem(int) error
	PauseItem(int) error
	ResumeItem(int) error
	AdvanceItem(*queue.Item)
	PickItem(stage queue.ItemStage) *queue.Item
	ExportItem(*queue.Item) error
}

type queueService struct {
	thea Thea
}

// GetAllItems returns all QueueItems currently managed by the queue service
func (service *queueService) GetAllItems() *[]*queue.Item {
	return service.thea.queue().Items()
}

// GetItem returns the QueueItem with the matching ID, if found
func (service *queueService) GetItem(itemID int) (*queue.Item, error) {
	item, position := service.thea.queue().FindById(itemID)
	if position == -1 || item == nil {
		return nil, fmt.Errorf("failed to GetItem(%d) -> No item with this ID exists", itemID)
	}
	return item, nil
}

// ReorderList accepts a list of IDs representing the desired ordering,
// and will reorder the internal data to match.
func (service *queueService) ReorderQueue(newOrder []int) error {
	if err := service.thea.queue().Reorder(newOrder); err != nil {
		return fmt.Errorf("failed to ReorderList(%v) -> %s", newOrder, err.Error())
	}

	return nil
}

// PromoteItem reorders the queue (via ReorderQueue) so that the provided
// ID is at index 0
func (service *queueService) PromoteItem(itemID int) error {
	item, idx := service.thea.queue().FindById(itemID)
	if item == nil || idx == -1 {
		return fmt.Errorf("failed to PromoteItem(%d) -> No item with this ID exists", itemID)
	} else if idx == 0 {
		return nil
	}

	newOrder := make([]int, 0)
	for _, item := range *service.GetAllItems() {
		newOrder = append(newOrder, item.ItemID)
	}

	if idx == len(newOrder)-1 {
		newOrder = append([]int{newOrder[idx]}, newOrder[:len(newOrder)-1]...)
	} else {
		extracted := append([]int{newOrder[idx]}, newOrder[:idx]...)
		newOrder = append(extracted, newOrder[idx+1:]...)
	}

	if err := service.thea.queue().Reorder(newOrder); err != nil {
		return fmt.Errorf("failed to PromoteItem(%d) -> %s", itemID, err.Error())
	}

	return nil
}

// CancelItem will cancel the item with the ID provided if it can be found. If the item is currently
// busy, it will be scheduled for cancellation (once the task is complete, the item will become cancelled)
func (service *queueService) CancelItem(itemID int) error {
	item, pos := service.thea.queue().FindById(itemID)
	if item == nil || pos == -1 {
		return fmt.Errorf("failed to CancelItem(%d) -> No item with this ID exists", itemID)
	}

	// Ensure that the item can be cancelled... If it can, but it's currently busy, mark
	// it as "Cancelling" so that the currently executing task can fully cancel it after
	// it's complete
	switch item.Status {
	case queue.Cancelled:
	case queue.Cancelling:
		return fmt.Errorf("failed to CancelItem(%d) -> Item is already cancelled", itemID)
	case queue.Pending:
	case queue.NeedsResolving:
		// Item is "Idle" so can be marked as cancelled immediattely
		item.SetStatus(queue.Cancelled)
	case queue.Completed:
		return fmt.Errorf("failed to CancelItem(%d) -> Item has already been completed", itemID)
	case queue.NeedsAttention:
	case queue.Processing:
		// Item is busy, mark as cancelling!
		item.SetStatus(queue.Cancelling)
	}

	// Cancel any/all ffmpeg instances for this item - all other tasks are super quick
	// to execute, so only the ffmpeg stage needs this "intervention" to cut the processing
	// off... otherwise we could be waiting for hours.
	for _, instance := range service.thea.ffmpeg().GetInstancesForItem(itemID) {
		instance.Cancel()
	}

	return nil
}

// PauseItem will pause a specified item if it can be found, and will
// also pause any associatted Ffmpeg instances.
func (service *queueService) PauseItem(itemID int) error {
	item, pos := service.thea.queue().FindById(itemID)
	if item == nil || pos == -1 {
		return fmt.Errorf("failed to PauseItem(%d) -> No item with this ID exists", itemID)
	}

	item.SetPaused(true)

	instances := service.thea.ffmpeg().GetInstancesForItem(itemID)
	for _, v := range instances {
		v.Pause()
	}

	return nil
}

// ResumeItem will resume an items progress by "unpausing" it. If all Ffmpeg Instances are
// paused at the time, they will also be resumed
func (service *queueService) ResumeItem(itemID int) error {
	item, pos := service.thea.queue().FindById(itemID)
	if item == nil || pos == -1 {
		return fmt.Errorf("failed to ResumeItem(%d) -> No item with this ID exists", itemID)
	} else if item.Status != queue.Paused {
		return fmt.Errorf("failed to ResumeItem(%d) -> Item is not paused", itemID)
	}

	item.SetPaused(false)

	// If all ffmpeg instances were paused then we can somewhat safely assume that unpausing
	// the item means we should unpause all instances too
	instances := service.thea.ffmpeg().GetInstancesForItem(itemID)
	for _, instance := range instances {
		if instance.Status() != ffmpeg.SUSPENDED {
			return nil
		}
	}
	for _, instance := range instances {
		instance.Resume()
	}

	return nil
}

func (service *queueService) AdvanceItem(item *queue.Item) {
	log.Emit(logger.DEBUG, "Advancing item %s to next stage\n", item)
	service.thea.queue().AdvanceStage(item)
	service.thea.workerPool().WakeupWorkers()
}

func (service *queueService) PickItem(stage queue.ItemStage) *queue.Item {
	return service.thea.queue().Pick(stage)
}

// ExportItem accepts a QueueItem, and will:
// 1. Form a database model with the item and it's completed ffmpeg instances.
// 2. Save this model to the persisted database.
// 3. Mark this item as *completed* in the queue.
func (service *queueService) ExportItem(item *queue.Item) error {
	if item.Stage != queue.Database || item.Status != queue.Processing {
		// Cannot export an item that is at any other stage!
		return fmt.Errorf("failed to ExportItem(%d) -> Item is not at correct stage/status", item.ItemID)
	}

	// Form a database model of the item which can be persisted. This differs from the standard item
	// as this will embue the data with more information (such as ffmpeg instances, export locations,
	// et cetera...). For the most part, this is just converting the data from the current structure (useful for
	// state-management), to another (useful for DB storage/lookup).
	exportItem := &export.ExportedItem{
		Name:          item.OmdbInfo.Title,
		Description:   item.OmdbInfo.Description,
		Runtime:       item.OmdbInfo.Runtime,
		ReleaseYear:   item.OmdbInfo.ReleaseYear,
		Image:         item.OmdbInfo.PosterUrl,
		Exports:       make([]*export.ExportDetail, 0),
		Genres:        nil,
		EpisodeNumber: nil,
		SeasonNumber:  nil,
		SeriesID:      nil,
		Series:        nil,
	}

	if item.TitleInfo.Episodic {
		if item.TitleInfo.Episode == -1 || item.TitleInfo.Season == -1 {
			return fmt.Errorf("failed to ExportItem(%d) -> Item declared itself as Episodic, however season/episode information is invalid", item.ItemID)
		}

		exportItem.EpisodeNumber = &item.TitleInfo.Episode
		exportItem.SeasonNumber = &item.TitleInfo.Season
		exportItem.Series = &export.Series{Name: item.TitleInfo.Title}
	}

	// Extract all genres and find pre-existing ones in the DB - for ones we could NOT find,
	// create them manually and omit the gorm model ID so that the genre is created a s new row
	db := database.DB.GetInstance()
	var exportGenres []*export.Genre
	isGenreNew := func(genre string) bool {
		for _, v := range exportGenres {
			if v.Name == genre {
				return false
			}
		}

		return true
	}

	db.Where("name in ?", item.OmdbInfo.Genre).Find(&exportGenres)
	for _, v := range item.OmdbInfo.Genre {
		// Add missing genres to the above result
		if isGenreNew(v) {
			exportGenres = append(exportGenres, &export.Genre{
				Name: v,
			})
		}
	}
	exportItem.Genres = exportGenres

	exports := service.thea.ffmpeg().GetInstancesForItem(item.ItemID)
	for _, v := range exports {
		if v.Status() != ffmpeg.COMPLETE {
			return fmt.Errorf("failed to ExportItem(%d) -> One or more FFmpeg instances are not finished (found instance %v as incomplete)", item.ItemID, v)
		}

		exportItem.Exports = append(exportItem.Exports, &export.ExportDetail{
			Name: v.Profile(),
			Path: v.OutputPath(),
		})
	}

	// Attempt to persist the formed exportItem to the database
	if err := database.DB.GetInstance().Debug().Save(exportItem).Error; err != nil {
		return fmt.Errorf("failed to ExportItem(%d) -> Database save operation FAILED: %s", item.ItemID, err.Error())
	}

	return nil
}

func NewQueueService(thea Thea) QueueService {
	return &queueService{
		thea: thea,
	}
}