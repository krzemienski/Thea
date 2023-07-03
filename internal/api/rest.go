package api

import (
	"context"
	"sync"

	"github.com/hbomb79/Thea/internal/api/ingests"
	"github.com/hbomb79/Thea/internal/api/medias"
	"github.com/hbomb79/Thea/internal/api/targets"
	"github.com/hbomb79/Thea/internal/api/transcodes"
	"github.com/hbomb79/Thea/internal/api/workflows"
	"github.com/hbomb79/Thea/internal/http/websocket"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

type (
	RestConfig struct {
		HostAddr string `toml:"host_address" env:"API_HOST_ADDR" env-default:"0.0.0.0:8080"`
	}

	controller interface {
		SetRoutes(*echo.Group)
	}

	// The RestGateway is a thin-wrapper around the Echo HTTP router. It's sole responsbility
	// is to create the routes Thea exposes, manage ongoing web socket connections and events,
	// and to enforce authc + authz middleware where applicable.
	RestGateway struct {
		*broadcaster
		config              *RestConfig
		ec                  *echo.Echo
		socket              *websocket.SocketHub
		ingestController    controller
		transcodeController controller
		targetsController   controller
		workflowController  controller
		mediaController     controller
	}
)

// NewRestGateway constructs the Echo router and populates it with all the
// routes defined by the various controllers. Each controller requires access
// to a data store, which are provided as arguments.
func NewRestGateway(
	config *RestConfig,
	ingestService ingests.Service,
	transcodeService transcodes.Service,
	transcodeStore transcodes.Store,
	targetStore targets.Store,
	workflowStore workflows.Store,
	mediaStore medias.Store,
) *RestGateway {
	ec := echo.New()
	ec.HidePort = true
	ec.HideBanner = true

	socket := websocket.New()
	gateway := &RestGateway{
		broadcaster:         newBroadcaster(socket, ingestService, mediaStore, targetStore, transcodeStore, workflowStore),
		config:              config,
		ec:                  ec,
		socket:              socket,
		ingestController:    ingests.New(ingestService),
		transcodeController: transcodes.New(transcodeService, transcodeStore),
		targetsController:   targets.New(targetStore),
		workflowController:  workflows.New(workflowStore),
		mediaController:     medias.New(mediaStore),
	}

	ec.Use(middleware.AddTrailingSlash())
	ec.Use(middleware.Logger())
	ec.Use(middleware.Recover())

	ec.GET("/api/thea/v1/activity/ws", func(ec echo.Context) error {
		gateway.socket.UpgradeToSocket(ec.Response(), ec.Request())
		return nil
	})

	ingests := ec.Group("/api/thea/v1/ingests")
	gateway.ingestController.SetRoutes(ingests)

	transcodes := ec.Group("/api/thea/v1/transcodes")
	gateway.transcodeController.SetRoutes(transcodes)

	transcodeTargets := ec.Group("/api/thea/v1/transcode-targets")
	gateway.targetsController.SetRoutes(transcodeTargets)

	transcodeWorkflows := ec.Group("/api/thea/v1/transcode-workflows")
	gateway.workflowController.SetRoutes(transcodeWorkflows)

	media := ec.Group("/api/thea/v1/media")
	gateway.mediaController.SetRoutes(media)

	return gateway
}

func (gateway *RestGateway) Run(parentCtx context.Context) error {
	ctx, ctxCancel := context.WithCancelCause(parentCtx)
	wg := &sync.WaitGroup{}

	// Start echo router
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := gateway.ec.Start(gateway.config.HostAddr); err != nil {
			ctxCancel(err)
		}
	}()

	// Start thread to listen for context cancellation
	go func(ec *echo.Echo) {
		<-ctx.Done()
		ec.Close()
	}(gateway.ec)

	// Start websocket
	wg.Add(1)
	go func() {
		defer wg.Done()
		gateway.socket.Start(ctx)
	}()

	wg.Wait()

	// Return cancellation cause if any, otherwise nil as parent context
	// cancellation is not an error case we should report.
	if cause := context.Cause(ctx); cause != ctx.Err() {
		return cause
	}

	return nil
}
