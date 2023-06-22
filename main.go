package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/hbomb79/Thea/internal"
	"github.com/hbomb79/Thea/internal/api"
	"github.com/hbomb79/Thea/pkg/logger"
	"github.com/hbomb79/Thea/pkg/socket"
)

var mainLogger = logger.Get("Main")

const VERSION = 0.8

type Thea interface {
	Start(ctx context.Context) error
}

type services struct {
	thea        Thea
	socketHub   *socket.SocketHub
	wsGateway   *api.WsGateway
	httpGateway *api.HttpGateway
	httpRouter  *api.Router
}

func NewTpa(config internal.TheaConfig) *services {
	services := &services{
		httpRouter: api.NewRouter(),
		socketHub:  socket.NewSocketHub(),
	}

	thea := internal.New(config)
	services.thea = thea
	services.wsGateway = api.NewWsGateway(thea)
	services.httpGateway = api.NewHttpGateway(thea)
	return services

}

func (serv *services) Start() {
	mainLogger.Emit(logger.INFO, " --- Starting Thea (version %v) ---\n", VERSION)
	exitChannel := make(chan os.Signal, 1)
	signal.Notify(exitChannel, os.Interrupt, syscall.SIGTERM)

	ctx, ctxCancel := context.WithCancel(context.Background())

	serv.setupRoutes()

	// Start websocket, router and Thea
	wg := &sync.WaitGroup{}
	wg.Add(3)
	go func() {
		defer wg.Done()
		serv.socketHub.Start()
	}()
	go func() {
		defer wg.Done()
		serv.httpRouter.Start(&api.RouterOptions{
			ApiPort: 8080,
			ApiHost: "0.0.0.0",
			ApiRoot: "/api/thea",
		})
	}()
	go func() {
		defer wg.Done()
		if err := serv.thea.Start(ctx); err != nil {
			mainLogger.Emit(logger.FATAL, "Failed to start Processor: %v", err.Error())
		}

		mainLogger.Emit(logger.STOP, "Processor shutdown, cleaning up supporting services...\n")
		serv.socketHub.Close()
		serv.httpRouter.Stop()
	}()

	// Wait for all processes to finish
	<-exitChannel
	ctxCancel()
	wg.Wait()
}

// setupRoutes initialises the routes and commands for the HTTP
// REST router, and the websocket hub
func (serv *services) setupRoutes() {
	serv.httpRouter.CreateRoute("v0/queue", "GET", serv.httpGateway.HttpQueueIndex)
	serv.httpRouter.CreateRoute("v0/queue/{id}", "GET", serv.httpGateway.HttpQueueGet)
	serv.httpRouter.CreateRoute("v0/queue/promote/{id}", "POST", serv.httpGateway.HttpQueueUpdate)
	serv.httpRouter.CreateRoute("v0/ws", "GET", serv.socketHub.UpgradeToSocket)

	serv.socketHub.BindCommand("QUEUE_INDEX", serv.wsGateway.WsQueueIndex)
	serv.socketHub.BindCommand("QUEUE_DETAILS", serv.wsGateway.WsQueueDetails)
	serv.socketHub.BindCommand("QUEUE_REORDER", serv.wsGateway.WsQueueReorder)
	serv.socketHub.BindCommand("TROUBLE_RESOLVE", serv.wsGateway.WsTroubleResolve)
	serv.socketHub.BindCommand("TROUBLE_DETAILS", serv.wsGateway.WsTroubleDetails)
	serv.socketHub.BindCommand("PROMOTE_ITEM", serv.wsGateway.WsItemPromote)
	serv.socketHub.BindCommand("PAUSE_ITEM", serv.wsGateway.WsItemPause)
	serv.socketHub.BindCommand("CANCEL_ITEM", serv.wsGateway.WsItemCancel)

	serv.socketHub.BindCommand("PROFILE_INDEX", serv.wsGateway.WsProfileIndex)
	serv.socketHub.BindCommand("PROFILE_CREATE", serv.wsGateway.WsProfileCreate)
	serv.socketHub.BindCommand("PROFILE_REMOVE", serv.wsGateway.WsProfileRemove)
	serv.socketHub.BindCommand("PROFILE_MOVE", serv.wsGateway.WsProfileMove)
	serv.socketHub.BindCommand("PROFILE_SET_MATCH_CONDITIONS", serv.wsGateway.WsProfileSetMatchConditions)
	serv.socketHub.BindCommand("PROFILE_UPDATE_COMMAND", serv.wsGateway.WsProfileUpdateCommand)
}

// func (serv *services) handleTheaUpdate(update *internal.Update) {
// 	body := map[string]interface{}{"context": update}
// 	if update.UpdateType == internal.PROFILE_UPDATE {
// 		body["profiles"] = serv.thea.GetAllProfiles()
// 		body["targetOpts"] = serv.thea.GetKnownFfmpegOptions()
// 	}

// 	mainLogger.Emit(logger.VERBOSE, "Emitting UPDATE message %#v\n", body)
// 	serv.socketHub.Send(&socket.SocketMessage{
// 		Title: "UPDATE",
// 		Body:  body,
// 		Type:  socket.Update,
// 	})
// }

// main() is the entry point to the program, from here will
// we load the users Thea configuration from their home directory,
// merging the configuration with the default config
func main() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Panicf(err.Error())
	}

	procCfg := new(internal.TheaConfig)
	if err := procCfg.LoadFromFile(filepath.Join(homeDir, ".config/thea/config.yaml")); err != nil {
		panic(err)
	}

	servs := NewTpa(*procCfg)
	servs.Start()
}
