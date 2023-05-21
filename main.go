package main

import (
	"crypto/rsa"
	"flag"
	"fmt"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"html/template"
	"net/http"
	"os"
	"path"
	"regexp"
	"sync"
)

const DEBUG = false

var bodyDump = middleware.BodyDump(func(c echo.Context, reqBody, resBody []byte) {
	fmt.Printf("%s\n", reqBody)
	fmt.Printf("%s\n", resBody)
})

type App struct {
	DB                          *gorm.DB
	Config                      *Config
	AnonymousLoginUsernameRegex *regexp.Regexp
	Constants                   *ConstantsType
	Key                         *rsa.PrivateKey
	KeyB3Sum512                 *[]byte
	SkinMutex                   *sync.Mutex
}

func handleError(err error, c echo.Context) {
	c.Logger().Error(err)
	if httpError, ok := err.(*echo.HTTPError); ok {
		if httpError.Code == http.StatusNotFound {
			if s, ok := httpError.Message.(string); ok {
				e := c.String(httpError.Code, s)
				Check(e)
				return
			}
		}
	}
	e := c.String(http.StatusInternalServerError, "Internal server error")
	Check(e)
}

func setupFrontRoutes(app *App, e *echo.Echo) {
	e.GET("/", FrontRoot(app))
	e.GET("/drasl/challenge-skin", FrontChallengeSkin(app))
	e.GET("/drasl/profile", FrontProfile(app))
	e.GET("/drasl/registration", FrontRegistration(app))
	e.POST("/drasl/delete-account", FrontDeleteAccount(app))
	e.POST("/drasl/login", FrontLogin(app))
	e.POST("/drasl/logout", FrontLogout(app))
	e.POST("/drasl/register", FrontRegister(app))
	e.POST("/drasl/update", FrontUpdate(app))
	e.Static("/drasl/public", path.Join(app.Config.DataDirectory, "public"))
	e.Static("/drasl/texture/cape", path.Join(app.Config.StateDirectory, "cape"))
	e.Static("/drasl/texture/skin", path.Join(app.Config.StateDirectory, "skin"))
}

func setupAuthRoutes(app *App, e *echo.Echo) {
	e.Any("/authenticate", AuthAuthenticate(app))
	e.Any("/invalidate", AuthInvalidate(app))
	e.Any("/refresh", AuthRefresh(app))
	e.Any("/signout", AuthSignout(app))
	e.Any("/validate", AuthValidate(app))
}

func setupAccountRoutes(app *App, e *echo.Echo) {
	e.GET("/user/security/location", AccountVerifySecurityLocation(app))
	e.GET("/users/profiles/minecraft/:playerName", AccountPlayerNameToID(app))
	e.POST("/profiles/minecraft", AccountPlayerNamesToIDs(app))
}

func setupSessionRoutes(app *App, e *echo.Echo) {
	e.Any("/session/minecraft/hasJoined", SessionHasJoined(app))
	e.Any("/session/minecraft/join", SessionJoin(app))
	e.Any("/session/minecraft/profile/:id", SessionProfile(app))
}

func setupServicesRoutes(app *App, e *echo.Echo) {
	e.Any("/player/attributes", ServicesPlayerAttributes(app))
	e.Any("/player/certificates", ServicesPlayerCertificates(app))
	e.Any("/user/profiles/:uuid/names", ServicesUUIDToNameHistory(app))
	e.DELETE("/minecraft/profile/capes/active", ServicesDeleteCape(app))
	e.DELETE("/minecraft/profile/skins/active", ServicesDeleteSkin(app))
	e.GET("/minecraft/profile", ServicesProfileInformation(app))
	e.GET("/minecraft/profile/name/:playerName/available", ServicesNameAvailability(app))
	e.GET("/minecraft/profile/namechange", ServicesNameChange(app))
	e.GET("/privacy/blocklist", ServicesBlocklist(app))
	e.GET("/rollout/v1/msamigration", ServicesMSAMigration(app))
	e.POST("/minecraft/profile/skins", ServicesUploadSkin(app))
	e.PUT("/minecraft/profile/name/:playerName", ServicesChangeName(app))
}

func GetUnifiedServer(app *App) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = app.Config.HideListenAddress
	e.HTTPErrorHandler = handleError
	if app.Config.LogRequests {
		e.Use(middleware.Logger())
	}
	if DEBUG {
		e.Use(bodyDump)
	}
	t := &Template{
		templates: template.Must(template.ParseGlob("view/*.html")),
	}
	e.Renderer = t

	setupFrontRoutes(app, e)
	setupAuthRoutes(app, e)
	setupAccountRoutes(app, e)
	setupSessionRoutes(app, e)
	setupServicesRoutes(app, e)

	return e
}

func GetFrontServer(app *App) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = app.Config.HideListenAddress
	e.HTTPErrorHandler = handleError
	if app.Config.LogRequests {
		e.Use(middleware.Logger())
	}
	if DEBUG {
		e.Use(bodyDump)
	}
	t := &Template{
		templates: template.Must(template.ParseGlob("view/*.html")),
	}
	e.Renderer = t
	setupFrontRoutes(app, e)
	return e
}

func GetAuthServer(app *App) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = app.Config.HideListenAddress
	e.HTTPErrorHandler = handleError
	if app.Config.LogRequests {
		e.Use(middleware.Logger())
	}
	if DEBUG {
		e.Use(bodyDump)
	}
	setupAuthRoutes(app, e)
	return e
}

func GetAccountServer(app *App) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = app.Config.HideListenAddress
	e.HTTPErrorHandler = handleError
	if app.Config.LogRequests {
		e.Use(middleware.Logger())
	}
	if DEBUG {
		e.Use(bodyDump)
	}
	setupAccountRoutes(app, e)
	return e
}

func GetSessionServer(app *App) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = app.Config.HideListenAddress
	e.HTTPErrorHandler = handleError
	if app.Config.LogRequests {
		e.Use(middleware.Logger())
	}
	if DEBUG {
		e.Use(bodyDump)
	}
	setupSessionRoutes(app, e)
	return e
}

func GetServicesServer(app *App) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = app.Config.HideListenAddress
	e.HTTPErrorHandler = handleError
	if app.Config.LogRequests {
		e.Use(middleware.Logger())
	}
	setupServicesRoutes(app, e)
	return e
}

func setup(config *Config) *App {
	key := ReadOrCreateKey(config)
	keyB3Sum512 := KeyB3Sum512(key)

	db_path := path.Join(config.StateDirectory, "drasl.db")
	db, err := gorm.Open(sqlite.Open(db_path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	Check(err)

	err = db.AutoMigrate(&User{})
	Check(err)

	err = db.AutoMigrate(&TokenPair{})
	Check(err)

	var anonymousLoginUsernameRegex *regexp.Regexp
	if config.AnonymousLogin.Allow {
		anonymousLoginUsernameRegex, err = regexp.Compile(config.AnonymousLogin.UsernameRegex)
		Check(err)
	}
	return &App{
		Config:                      config,
		AnonymousLoginUsernameRegex: anonymousLoginUsernameRegex,
		Constants:                   Constants,
		DB:                          db,
		Key:                         key,
		KeyB3Sum512:                 &keyB3Sum512,
	}
}

func runServer(e *echo.Echo, listenAddress string) {
	e.Logger.Fatal(e.Start(listenAddress))
}

func main() {
	defaultConfigPath := path.Join(Constants.ConfigDirectory, "config.toml")

	configPath := flag.String("config", defaultConfigPath, "Path to config file")
	help := flag.Bool("help", false, "Show help message")
	flag.Parse()

	if *help {
		fmt.Println("Usage: drasl [options]")
		fmt.Println("Options:")
		flag.PrintDefaults()
		os.Exit(0)
	}

	config := ReadOrCreateConfig(*configPath)
	app := setup(config)

	if app.Config.UnifiedServer != nil {
		go runServer(GetUnifiedServer(app), app.Config.UnifiedServer.ListenAddress)
	} else {
		go runServer(GetFrontServer(app), app.Config.FrontEndServer.ListenAddress)
		go runServer(GetAuthServer(app), app.Config.AuthServer.ListenAddress)
		go runServer(GetAccountServer(app), app.Config.AccountServer.ListenAddress)
		go runServer(GetSessionServer(app), app.Config.SessionServer.ListenAddress)
		go runServer(GetServicesServer(app), app.Config.ServicesServer.ListenAddress)
	}
	select {}
}
