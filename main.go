package main

import (
	"context"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/gin-gonic/contrib/static"
	"github.com/gin-gonic/gin"
	"github.com/iridiumdev/gin-jwt"
	"github.com/iridiumdev/webwallet-core/auth"
	"github.com/iridiumdev/webwallet-core/config"
	"github.com/iridiumdev/webwallet-core/event"
	"github.com/iridiumdev/webwallet-core/user"
	"github.com/iridiumdev/webwallet-core/wallet"
	log "github.com/sirupsen/logrus"
	"github.com/toorop/gin-logrus"
	"gopkg.in/mgo.v2"
	"net/http"
	"strings"
)

func main() {

	log.SetFormatter(&log.TextFormatter{})
	log.SetLevel(log.TraceLevel)

	mongoSession := initMongoClient()
	dockerClient := initDockerClient()

	initStores(mongoSession)
	userService, _, eventService := initServices(dockerClient)

	statusWatcher := wallet.InitWatcher(dockerClient, eventService)

	engine, _, _ := initMainEngine(userService)

	statusWatcher.Run() // TODO: daniel 29.11.18 - do something with the returned chan - e.g. use in a websocket event dispatcher
	engine.Run(config.Get().Server.Address)

	defer mongoSession.Close()
	defer dockerClient.Close()
	defer statusWatcher.Close()
}

func initDockerClient() *client.Client {

	log.Info("Initializing docker client")

	// TODO: daniel 10.11.18 - allow to pass a custom config, e.g. for docker swarm
	cli, err := client.NewEnvClient()
	if err != nil {
		panic(err)
	}

	log.Infof("Initialized docker client %s", cli.ClientVersion())

	ctx := context.Background()

	log.Infof("Pulling satellite docker image %s", config.Get().Webwallet.Satellite.Image)
	if _, err := cli.ImagePull(ctx, config.Get().Webwallet.Satellite.Image, types.ImagePullOptions{}); err != nil {
		panic(err)
	}

	return cli
}

func initMongoClient() *mgo.Session {

	log.Infof("Initializing mongodb connection to server(s): %s", config.Get().Mongo.Address)

	session, err := mgo.Dial(config.Get().Mongo.Address)
	if err != nil {
		panic(err)
	}

	info, err := session.BuildInfo()
	if err != nil {
		log.Error("Could not retrieve any build information from mongodb server!")
	} else {
		log.Infof("Initialized mongodb connection %s", info.Version)
		log.Debug(info)
	}

	// Optional. Switch the session to a monotonic behavior.
	session.SetMode(mgo.Monotonic, true)

	return session
}

func initServices(dockerClient *client.Client) (user.Service, wallet.Service, event.Service) {

	userService := user.InitService()

	walletService := wallet.InitService(dockerClient)

	eventService := event.InitService()

	return userService, walletService, eventService

}

func initStores(session *mgo.Session) {

	wallet.InitStore(session.Clone().DB(config.Get().Mongo.Database))
	user.InitStore(session.Clone().DB(config.Get().Mongo.Database))

}

func initMainEngine(userService user.Service) (*gin.Engine, *gin.RouterGroup, *jwt.GinJWTMiddleware) {

	engine := gin.Default()
	engine.Use(ginlogrus.Logger(log.StandardLogger()), gin.Recovery())
	engine.Use(static.Serve("/", static.LocalFile(config.Get().Server.StaticLocation, true)))

	engine.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/api") {
			c.JSON(http.StatusNotFound, gin.H{
				"error":  "That is not the API you are looking for.",
				"status": http.StatusNotFound,
			})
		} else {
			c.File(config.Get().Server.StaticLocation + "/index.html")
		}
	})

	authMiddleware := auth.InitMiddleware(userService)

	authApi := engine.Group("/auth")
	authApi.POST("/login", authMiddleware.LoginHandler)
	authApi.POST("/refresh", authMiddleware.RefreshHandler)

	api := engine.Group("/api/v1")
	api.Use(authMiddleware.MiddlewareFunc())

	initDependencyTree(api, authApi)

	return engine, api, authMiddleware
}

func initDependencyTree(api *gin.RouterGroup, authApi *gin.RouterGroup) {
	userController := user.NewController(api, authApi)
	userController.Routes()

	walletController := wallet.NewController(api)
	walletController.Routes()

	eventController := event.NewController(api)
	eventController.Routes()
}
