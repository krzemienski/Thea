package internal

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/hbomb79/Thea/internal/database"
	"github.com/hbomb79/Thea/internal/ffmpeg"
	"github.com/hbomb79/Thea/internal/media"
	"github.com/hbomb79/Thea/internal/transcode"
	"github.com/hbomb79/Thea/internal/workflow"
	"github.com/hbomb79/Thea/internal/workflow/match"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

const (
	PgFkConstraintViolationCode = "23503"
)

var (
	ErrDatabaseNotConnected    = errors.New("cannot construct thea data store with a disconnected db")
	ErrWorkflowTargetIDMissing = errors.New("one or more of the targets provided cannot be found")
)

type (
	// storeOrchestrator is responsible for managing all of Thea's resources,
	// especially highly-relational data. You can think of all
	// the data stores below this layer being 'dumb', and this store
	// linking them together and providing the database instance
	//
	// If consumers need to be able to access data stores directly, they're
	// welcome to do so - however caution should be taken as stores have no
	// obligation to take care of relational data (which is the orchestrator's job)
	storeOrchestrator struct {
		db             database.Manager
		mediaStore     *media.Store
		transcodeStore *transcode.Store
		workflowStore  *workflow.Store
		targetStore    *ffmpeg.Store
	}
)

func NewStoreOrchestrator(db database.Manager) (*storeOrchestrator, error) {
	if db.GetSqlxDb() == nil {
		return nil, ErrDatabaseNotConnected
	}

	return &storeOrchestrator{
		db:             db,
		mediaStore:     &media.Store{},
		transcodeStore: &transcode.Store{},
		workflowStore:  &workflow.Store{},
		targetStore:    &ffmpeg.Store{},
	}, nil
}

func (orchestrator *storeOrchestrator) GetMedia(mediaId uuid.UUID) *media.Container {
	return orchestrator.mediaStore.GetMedia(orchestrator.db.GetSqlxDb(), mediaId)
}

func (orchestrator *storeOrchestrator) GetMovie(movieId uuid.UUID) (*media.Movie, error) {
	return orchestrator.mediaStore.GetMovie(orchestrator.db.GetSqlxDb(), movieId)
}

func (orchestrator *storeOrchestrator) GetEpisode(episodeId uuid.UUID) (*media.Episode, error) {
	return orchestrator.mediaStore.GetEpisode(orchestrator.db.GetSqlxDb(), episodeId)
}

func (orchestrator *storeOrchestrator) GetEpisodeWithTmdbId(tmdbID string) (*media.Episode, error) {
	return orchestrator.mediaStore.GetEpisodeWithTmdbId(orchestrator.db.GetSqlxDb(), tmdbID)
}

func (orchestrator *storeOrchestrator) GetSeason(seasonId uuid.UUID) (*media.Season, error) {
	return orchestrator.mediaStore.GetSeason(orchestrator.db.GetSqlxDb(), seasonId)
}

func (orchestrator *storeOrchestrator) GetSeasonWithTmdbId(tmdbID string) (*media.Season, error) {
	return orchestrator.mediaStore.GetSeasonWithTmdbId(orchestrator.db.GetSqlxDb(), tmdbID)
}

func (orchestrator *storeOrchestrator) GetSeries(seriesId uuid.UUID) (*media.Series, error) {
	return orchestrator.mediaStore.GetSeries(orchestrator.db.GetSqlxDb(), seriesId)
}

func (orchestrator *storeOrchestrator) GetSeriesWithTmdbId(tmdbID string) (*media.Series, error) {
	return orchestrator.mediaStore.GetSeriesWithTmdbId(orchestrator.db.GetSqlxDb(), tmdbID)
}

func (orchestrator *storeOrchestrator) GetAllMediaSourcePaths() ([]string, error) {
	return orchestrator.mediaStore.GetAllSourcePaths(orchestrator.db.GetSqlxDb())
}

func (orchestrator *storeOrchestrator) SaveMovie(movie *media.Movie) error {
	return orchestrator.mediaStore.SaveMovie(orchestrator.db.GetSqlxDb(), movie)
}

func (orchestrator *storeOrchestrator) SaveSeries(series *media.Series) error {
	return orchestrator.mediaStore.SaveSeries(orchestrator.db.GetSqlxDb(), series)
}

func (orchestrator *storeOrchestrator) SaveSeason(season *media.Season) error {
	return orchestrator.mediaStore.SaveSeason(orchestrator.db.GetSqlxDb(), season)
}

// SaveEpisode transactionally saves the episode provided, as well as the season and series
// it's associatted with. Existing models are updating ON CONFLICT with the TmdbID unique
// identifier. The PK's and relational FK's of the models will automatically be
// set during saving.
//
// Note: If the season/series are not provided, and the FK-constraint of the episode cannot
// be fulfilled because of this, then the save will fail. It is recommended to supply all parameters.
func (orchestrator *storeOrchestrator) SaveEpisode(episode *media.Episode, season *media.Season, series *media.Series) error {
	// Store old PK/FKs so we can rollback on transaction failure
	episodeId := episode.ID
	seasonId := season.ID
	seriesId := series.ID
	episodeFk := episode.SeasonID
	seasonFk := season.SeriesID

	if err := orchestrator.db.WrapTx(func(tx *sqlx.Tx) error {
		log.Verbosef("Saving series %#v\n", series)
		if err := orchestrator.mediaStore.SaveSeries(tx, series); err != nil {
			return err
		}

		log.Verbosef("Saving season %#v with series_id=%s\n", season, series.ID)
		season.SeriesID = series.ID
		if err := orchestrator.mediaStore.SaveSeason(tx, season); err != nil {
			return err
		}

		log.Verbosef("Saving episode %#v with season_id=%s\n", episode, seasonId)
		episode.SeasonID = season.ID
		return orchestrator.mediaStore.SaveEpisode(tx, episode)
	}); err != nil {
		log.Warnf(
			"Episode save failed, rolling back model keys (epID=%s, epFK=%s, seasonID=%s, seasonFK=%s, seriesID=%s)",
			episodeId, episodeFk, seasonId, seasonFk, seriesId,
		)

		episode.ID = episodeId
		season.ID = seasonId
		series.ID = seriesId

		episode.SeasonID = episodeFk
		season.SeriesID = seasonFk
		return err
	}

	return nil
}

// Workflows

// CreateWorkflow uses the information provided to construct and save a new workflow
// in a single DB transaction.
//
// Error will be returned if any of the target IDs provided do not refer to existing Target
// DB entries, or if the workflow infringes on any uniqueness constraints (label)
func (orchestrator *storeOrchestrator) CreateWorkflow(workflowID uuid.UUID, label string, criteria []match.Criteria, targetIDs []uuid.UUID, enabled bool) (*workflow.Workflow, error) {
	db := orchestrator.db.GetSqlxDb()
	if err := orchestrator.workflowStore.Create(db, workflowID, label, enabled, targetIDs, criteria); err != nil {
		return nil, err
	}

	return orchestrator.workflowStore.Get(db, workflowID), nil
}

// UpdateWorkflow transactionally updates an existing Workflow model
// using the optional paramaters provided. If a param is `nil` then the
// corresponding value in the model is NOT changed.
func (orchestrator *storeOrchestrator) UpdateWorkflow(workflowID uuid.UUID, newLabel *string, newCriteria *[]match.Criteria, newTargetIDs *[]uuid.UUID, newEnabled *bool) (*workflow.Workflow, error) {
	fail := func(desc string, err error) error {
		if pqErr, ok := err.(*pq.Error); ok {
			if pqErr.Code == PgFkConstraintViolationCode && pqErr.Table == "workflow_transcode_targets" {
				log.Debugf("DB query failure; apparent target ID FK violation %#v\n", err)
				return ErrWorkflowTargetIDMissing
			}
		}

		log.Errorf("Unexpected query failure: %v\n", err)
		return fmt.Errorf("failed to %s due to unexpected query error: %w", desc, err)
	}

	err := orchestrator.db.WrapTx(func(tx *sqlx.Tx) error {
		if newLabel != nil || newEnabled != nil {
			if err := orchestrator.workflowStore.UpdateWorkflowTx(tx, workflowID, newLabel, newEnabled); err != nil {
				return fail("update workflow row", err)
			}
		}
		if newCriteria != nil {
			if err := orchestrator.workflowStore.UpdateWorkflowCriteriaTx(tx, workflowID, *newCriteria); err != nil {
				return fail("update workflow criteria associations", err)
			}
		}
		if newTargetIDs != nil {
			if err := orchestrator.workflowStore.UpdateWorkflowTargetsTx(tx, workflowID, *newTargetIDs); err != nil {
				return fail("update workflow target associations", err)
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return orchestrator.workflowStore.Get(orchestrator.db.GetSqlxDb(), workflowID), nil
}

func (orchestrator *storeOrchestrator) GetWorkflow(id uuid.UUID) *workflow.Workflow {
	return orchestrator.workflowStore.Get(orchestrator.db.GetSqlxDb(), id)
}

func (orchestrator *storeOrchestrator) GetAllWorkflows() []*workflow.Workflow {
	all := orchestrator.workflowStore.GetAll(orchestrator.db.GetSqlxDb())
	return all
}

func (orchestrator *storeOrchestrator) DeleteWorkflow(id uuid.UUID) {
	orchestrator.workflowStore.Delete(orchestrator.db.GetSqlxDb(), id)
}

// Transcodes

func (orchestrator *storeOrchestrator) SaveTranscode(transcode *transcode.TranscodeTask) error {
	return orchestrator.transcodeStore.SaveTranscode(orchestrator.db.GetSqlxDb(), transcode)
}
func (orchestrator *storeOrchestrator) GetTranscode(id uuid.UUID) *transcode.TranscodeTask {
	return orchestrator.transcodeStore.Get(orchestrator.db.GetSqlxDb(), id)
}
func (orchestrator *storeOrchestrator) GetAllTranscodes() ([]*transcode.TranscodeTask, error) {
	return orchestrator.transcodeStore.GetAll(orchestrator.db.GetSqlxDb())
}
func (orchestrator *storeOrchestrator) GetTranscodesForMedia(mediaId uuid.UUID) ([]*transcode.TranscodeTask, error) {
	return orchestrator.transcodeStore.GetForMedia(orchestrator.db.GetSqlxDb(), mediaId)
}

// Targets

func (orchestrator *storeOrchestrator) SaveTarget(target *ffmpeg.Target) error {
	return orchestrator.targetStore.Save(orchestrator.db.GetSqlxDb(), target)
}

func (orchestrator *storeOrchestrator) GetTarget(id uuid.UUID) *ffmpeg.Target {
	return orchestrator.targetStore.Get(orchestrator.db.GetSqlxDb(), id)
}

func (orchestrator *storeOrchestrator) GetAllTargets() []*ffmpeg.Target {
	return orchestrator.targetStore.GetAll(orchestrator.db.GetSqlxDb())
}

func (orchestrator *storeOrchestrator) GetManyTargets(ids ...uuid.UUID) []*ffmpeg.Target {
	return orchestrator.targetStore.GetMany(orchestrator.db.GetSqlxDb(), ids...)
}

func (orchestrator *storeOrchestrator) DeleteTarget(id uuid.UUID) {
	orchestrator.targetStore.Delete(orchestrator.db.GetSqlxDb(), id)
}
