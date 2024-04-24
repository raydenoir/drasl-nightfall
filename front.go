package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"html/template"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"lukechampine.com/blake3"
	"net/http"
	"net/url"
	"os"
	"path"
)

/*
Web front end for creating user accounts, changing passwords, skins, player names, etc.
*/

const BROWSER_TOKEN_AGE_SEC = 24 * 60 * 60

// Must be in a region of the skin that supports translucency
const SKIN_WINDOW_X_MIN = 40
const SKIN_WINDOW_X_MAX = 48
const SKIN_WINDOW_Y_MIN = 9
const SKIN_WINDOW_Y_MAX = 11

// https://echo.labstack.com/guide/templates/
// https://stackoverflow.com/questions/36617949/how-to-use-base-template-file-for-golang-html-template/69244593#69244593
type Template struct {
	Templates map[string]*template.Template
}

func NewTemplate(app *App) *Template {
	t := &Template{
		Templates: make(map[string]*template.Template),
	}

	templateDir := path.Join(app.Config.DataDirectory, "view")

	names := []string{
		"root",
		"profile",
		"registration",
		"challenge-skin",
		"admin",
	}

	funcMap := template.FuncMap{
		"UserSkinURL":    app.UserSkinURL,
		"InviteURL":      app.InviteURL,
		"IsDefaultAdmin": app.IsDefaultAdmin,
	}

	for _, name := range names {
		tmpl := Unwrap(template.New("").Funcs(funcMap).ParseFiles(
			path.Join(templateDir, "layout.tmpl"),
			path.Join(templateDir, name+".tmpl"),
			path.Join(templateDir, "header.tmpl"),
			path.Join(templateDir, "footer.tmpl"),
		))
		t.Templates[name] = tmpl
	}

	return t
}

func (t *Template) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	return t.Templates[name].ExecuteTemplate(w, "base", data)
}

func setSuccessMessage(c *echo.Context, message string) {
	(*c).SetCookie(&http.Cookie{
		Name:     "successMessage",
		Value:    url.QueryEscape(message),
		Path:     "/",
		SameSite: http.SameSiteStrictMode,
		HttpOnly: true,
	})
}

// Set a warning message
func setWarningMessage(c *echo.Context, message string) {
	(*c).SetCookie(&http.Cookie{
		Name:     "warningMessage",
		Value:    url.QueryEscape(message),
		Path:     "/",
		SameSite: http.SameSiteStrictMode,
		HttpOnly: true,
	})
}

// Set an error message cookie
func setErrorMessage(c *echo.Context, message string) {
	(*c).SetCookie(&http.Cookie{
		Name:     "errorMessage",
		Value:    url.QueryEscape(message),
		Path:     "/",
		SameSite: http.SameSiteStrictMode,
		HttpOnly: true,
	})
}

func lastSuccessMessage(c *echo.Context) string {
	cookie, err := (*c).Cookie("successMessage")
	if err != nil || cookie.Value == "" {
		return ""
	}
	decoded, err := url.QueryUnescape(cookie.Value)
	if err != nil {
		return ""
	}
	setSuccessMessage(c, "")
	return decoded
}

func lastWarningMessage(c *echo.Context) string {
	cookie, err := (*c).Cookie("warningMessage")
	if err != nil || cookie.Value == "" {
		return ""
	}
	decoded, err := url.QueryUnescape(cookie.Value)
	if err != nil {
		return ""
	}
	setWarningMessage(c, "")
	return decoded
}

// Read and clear the error message cookie
func lastErrorMessage(c *echo.Context) string {
	cookie, err := (*c).Cookie("errorMessage")
	if err != nil || cookie.Value == "" {
		return ""
	}
	decoded, err := url.QueryUnescape(cookie.Value)
	if err != nil {
		return ""
	}
	setErrorMessage(c, "")
	return decoded
}

func getReturnURL(app *App, c *echo.Context) string {
	if (*c).FormValue("returnUrl") != "" {
		return (*c).FormValue("returnUrl")
	}
	if (*c).QueryParam("returnUrl") != "" {
		return (*c).QueryParam("username")
	}
	return app.FrontEndURL
}

// Authenticate a user using the `browserToken` cookie, and call `f` with a
// reference to the user
func withBrowserAuthentication(app *App, requireLogin bool, f func(c echo.Context, user *User) error) func(c echo.Context) error {
	return func(c echo.Context) error {
		returnURL := getReturnURL(app, &c)
		cookie, err := c.Cookie("browserToken")

		var user User
		if err != nil || cookie.Value == "" {
			if requireLogin {
				setErrorMessage(&c, "You are not logged in.")
				return c.Redirect(http.StatusSeeOther, returnURL)
			}
			return f(c, nil)
		} else {
			result := app.DB.First(&user, "browser_token = ?", cookie.Value)
			if result.Error != nil {
				if errors.Is(result.Error, gorm.ErrRecordNotFound) {
					if requireLogin {
						c.SetCookie(&http.Cookie{
							Name:     "browserToken",
							Value:    "",
							MaxAge:   -1,
							Path:     "/",
							SameSite: http.SameSiteStrictMode,
							HttpOnly: true,
						})
						setErrorMessage(&c, "You are not logged in.")
						return c.Redirect(http.StatusSeeOther, returnURL)
					}
					return f(c, nil)
				}
				return err
			}
			return f(c, &user)
		}
	}
}

func withBrowserAdmin(app *App, f func(c echo.Context, user *User) error) func(c echo.Context) error {
	return withBrowserAuthentication(app, true, func(c echo.Context, user *User) error {
		returnURL := getReturnURL(app, &c)

		if !user.IsAdmin {
			setErrorMessage(&c, "You are not an admin.")
			return c.Redirect(http.StatusSeeOther, returnURL)
		}

		return f(c, user)
	})
}

// GET /
func FrontRoot(app *App) func(c echo.Context) error {
	type rootContext struct {
		App            *App
		User           *User
		URL            string
		SuccessMessage string
		WarningMessage string
		ErrorMessage   string
	}

	return withBrowserAuthentication(app, false, func(c echo.Context, user *User) error {
		return c.Render(http.StatusOK, "root", rootContext{
			App:            app,
			User:           user,
			URL:            c.Request().URL.RequestURI(),
			SuccessMessage: lastSuccessMessage(&c),
			WarningMessage: lastWarningMessage(&c),
			ErrorMessage:   lastErrorMessage(&c),
		})
	})
}

type webManifestIcon struct {
	Src   string `json:"src"`
	Type  string `json:"type"`
	Sizes string `json:"sizes"`
}

type webManifest struct {
	Icons []webManifestIcon `json:"icons"`
}

func FrontWebManifest(app *App) func(c echo.Context) error {
	url, err := url.JoinPath(app.FrontEndURL, "web/icon.png")
	Check(err)

	manifest := webManifest{
		Icons: []webManifestIcon{{
			Src:   url,
			Type:  "image/png",
			Sizes: "512x512",
		}},
	}
	manifestBlob := Unwrap(json.Marshal(manifest))
	return func(c echo.Context) error {
		return c.JSONBlob(http.StatusOK, manifestBlob)
	}
}

// GET /registration
func FrontRegistration(app *App) func(c echo.Context) error {
	type context struct {
		App            *App
		User           *User
		URL            string
		SuccessMessage string
		WarningMessage string
		ErrorMessage   string
		InviteCode     string
	}

	return withBrowserAuthentication(app, false, func(c echo.Context, user *User) error {
		inviteCode := c.QueryParam("invite")
		return c.Render(http.StatusOK, "registration", context{
			App:            app,
			User:           user,
			URL:            c.Request().URL.RequestURI(),
			SuccessMessage: lastSuccessMessage(&c),
			WarningMessage: lastWarningMessage(&c),
			ErrorMessage:   lastErrorMessage(&c),
			InviteCode:     inviteCode,
		})
	})
}

// GET /web/admin
func FrontAdmin(app *App) func(c echo.Context) error {
	type adminContext struct {
		App            *App
		User           *User
		URL            string
		SuccessMessage string
		WarningMessage string
		ErrorMessage   string
		Users          []User
		Invites        []Invite
	}

	return withBrowserAdmin(app, func(c echo.Context, user *User) error {
		var users []User
		result := app.DB.Find(&users)
		if result.Error != nil {
			return result.Error
		}

		var invites []Invite
		result = app.DB.Find(&invites)
		if result.Error != nil {
			return result.Error
		}

		return c.Render(http.StatusOK, "admin", adminContext{
			App:            app,
			User:           user,
			URL:            c.Request().URL.RequestURI(),
			SuccessMessage: lastSuccessMessage(&c),
			WarningMessage: lastWarningMessage(&c),
			ErrorMessage:   lastErrorMessage(&c),
			Users:          users,
			Invites:        invites,
		})
	})
}

// POST /web/admin/delete-invite
func FrontDeleteInvite(app *App) func(c echo.Context) error {
	returnURL := Unwrap(url.JoinPath(app.FrontEndURL, "web/admin"))

	return withBrowserAdmin(app, func(c echo.Context, user *User) error {
		inviteCode := c.FormValue("inviteCode")

		var invite Invite
		result := app.DB.Where("code = ?", inviteCode).Delete(&invite)
		if result.Error != nil {
			return result.Error
		}

		return c.Redirect(http.StatusSeeOther, returnURL)
	})
}

// POST /web/admin/update-users
func FrontUpdateUsers(app *App) func(c echo.Context) error {
	return withBrowserAdmin(app, func(c echo.Context, user *User) error {
		returnURL := getReturnURL(app, &c)

		var users []User
		result := app.DB.Find(&users)
		if result.Error != nil {
			return result.Error
		}

		tx := app.DB.Begin()
		defer tx.Rollback()

		anyUnlockedAdmins := false
		for _, user := range users {
			shouldBeAdmin := c.FormValue("admin-"+user.Username) == "on"
			if app.IsDefaultAdmin(&user) {
				shouldBeAdmin = true
			}

			shouldBeLocked := c.FormValue("locked-"+user.Username) == "on"
			if shouldBeAdmin && !shouldBeLocked {
				anyUnlockedAdmins = true
			}
			if user.IsAdmin != shouldBeAdmin || user.IsLocked != shouldBeLocked {
				user.IsAdmin = shouldBeAdmin
				err := app.SetIsLocked(tx, &user, shouldBeLocked)
				if err != nil {
					return err
				}
				tx.Save(user)
			}
		}

		if !anyUnlockedAdmins {
			setErrorMessage(&c, "There must be at least one unlocked admin account.")
			return c.Redirect(http.StatusSeeOther, returnURL)
		}

		tx.Commit()

		setSuccessMessage(&c, "Changes saved.")
		return c.Redirect(http.StatusSeeOther, returnURL)
	})
}

// POST /web/admin/new-invite
func FrontNewInvite(app *App) func(c echo.Context) error {
	return withBrowserAdmin(app, func(c echo.Context, user *User) error {
		returnURL := getReturnURL(app, &c)

		_, err := app.CreateInvite()
		if err != nil {
			setErrorMessage(&c, "Error creating new invite.")
			return c.Redirect(http.StatusSeeOther, returnURL)
		}

		return c.Redirect(http.StatusSeeOther, returnURL)
	})
}

// GET /drasl/profile
func FrontProfile(app *App) func(c echo.Context) error {
	type profileContext struct {
		App            *App
		User           *User
		URL            string
		SuccessMessage string
		WarningMessage string
		ErrorMessage   string
		ProfileUser    *User
		ProfileUserID  string
		SkinURL        *string
		CapeURL        *string
		AdminView      bool
	}

	return withBrowserAuthentication(app, true, func(c echo.Context, user *User) error {
		var profileUser *User
		profileUsername := c.QueryParam("user")
		adminView := false
		if profileUsername == "" || profileUsername == user.Username {
			profileUser = user
		} else {
			if !user.IsAdmin {
				setErrorMessage(&c, "You are not an admin.")
				return c.Redirect(http.StatusSeeOther, app.FrontEndURL)
			}
			var profileUserStruct User
			result := app.DB.First(&profileUserStruct, "username = ?", profileUsername)
			profileUser = &profileUserStruct
			if result.Error != nil {
				setErrorMessage(&c, "User not found.")
				returnURL, err := url.JoinPath(app.FrontEndURL, "web/admin")
				if err != nil {
					return err
				}
				return c.Redirect(http.StatusSeeOther, returnURL)
			}
			adminView = true
		}

		skinURL, err := app.GetSkinURL(profileUser)
		if err != nil {
			return err
		}
		capeURL, err := app.GetCapeURL(profileUser)
		if err != nil {
			return err
		}

		id, err := UUIDToID(profileUser.UUID)
		if err != nil {
			return err
		}

		return c.Render(http.StatusOK, "profile", profileContext{
			App:            app,
			User:           user,
			URL:            c.Request().URL.RequestURI(),
			SuccessMessage: lastSuccessMessage(&c),
			WarningMessage: lastWarningMessage(&c),
			ErrorMessage:   lastErrorMessage(&c),
			ProfileUser:    profileUser,
			ProfileUserID:  id,
			SkinURL:        skinURL,
			CapeURL:        capeURL,
			AdminView:      adminView,
		})
	})
}

func nilIfEmpty(str string) *string {
	if str == "" {
		return nil
	}
	return &str
}

// POST /update
func FrontUpdate(app *App) func(c echo.Context) error {
	return withBrowserAuthentication(app, true, func(c echo.Context, user *User) error {
		returnURL := getReturnURL(app, &c)

		profileUUID := nilIfEmpty(c.FormValue("uuid"))
		playerName := nilIfEmpty(c.FormValue("playerName"))
		fallbackPlayer := nilIfEmpty(c.FormValue("fallbackPlayer"))
		password := nilIfEmpty(c.FormValue("password"))
		resetAPIToken := c.FormValue("resetApiToken") == "on"
		preferredLanguage := nilIfEmpty(c.FormValue("preferredLanguage"))
		skinModel := nilIfEmpty(c.FormValue("skinModel"))
		skinURL := nilIfEmpty(c.FormValue("skinUrl"))
		deleteSkin := c.FormValue("deleteSkin") == "on"
		capeURL := nilIfEmpty(c.FormValue("capeUrl"))
		deleteCape := c.FormValue("deleteCape") == "on"

		var profileUser *User
		if profileUUID == nil || *profileUUID == user.UUID {
			profileUser = user
		} else {
			if !user.IsAdmin {
				setErrorMessage(&c, "You are not an admin.")
				return c.Redirect(http.StatusSeeOther, app.FrontEndURL)
			}
			var profileUserStruct User
			result := app.DB.First(&profileUserStruct, "uuid = ?", profileUUID)
			profileUser = &profileUserStruct
			if result.Error != nil {
				setErrorMessage(&c, "User not found.")
				return c.Redirect(http.StatusSeeOther, returnURL)
			}
		}

		// Skin
		var skinReader *io.Reader
		skinFile, skinFileErr := c.FormFile("skinFile")
		if skinFileErr == nil {
			var err error
			skinHandle, err := skinFile.Open()
			if err != nil {
				return err
			}
			defer skinHandle.Close()
			var skinFileReader io.Reader = skinHandle
			skinReader = &skinFileReader
		}

		// Cape
		var capeReader *io.Reader
		capeFile, capeFileErr := c.FormFile("capeFile")
		if capeFileErr == nil {
			var err error
			capeHandle, err := capeFile.Open()
			if err != nil {
				return err
			}
			defer capeHandle.Close()
			var capeFileReader io.Reader = capeHandle
			capeReader = &capeFileReader
		}

		_, err := app.UpdateUser(
			user,         // caller
			*profileUser, // user
			password,
			nil, // isAdmin
			nil, // isLocked
			playerName,
			fallbackPlayer,
			resetAPIToken,
			preferredLanguage,
			skinModel,
			skinReader,
			skinURL,
			deleteSkin,
			capeReader,
			capeURL,
			deleteCape,
		)
		if err != nil {
			setErrorMessage(&c, err.Error())
			return c.Redirect(http.StatusSeeOther, returnURL)
		}

		setSuccessMessage(&c, "Changes saved.")
		return c.Redirect(http.StatusSeeOther, returnURL)
	})
}

// POST /logout
func FrontLogout(app *App) func(c echo.Context) error {
	return withBrowserAuthentication(app, true, func(c echo.Context, user *User) error {
		returnURL := app.FrontEndURL
		c.SetCookie(&http.Cookie{
			Name:     "browserToken",
			Value:    "",
			MaxAge:   -1,
			Path:     "/",
			SameSite: http.SameSiteStrictMode,
			HttpOnly: true,
		})
		user.BrowserToken = MakeNullString(nil)
		app.DB.Save(user)
		return c.Redirect(http.StatusSeeOther, returnURL)
	})
}

func getChallenge(app *App, username string, token string) []byte {
	// This challenge is nice because:
	// - it doesn't depend on any serverside state
	// - an attacker can't use it to verify a different username, since hash
	// incorporates the username - an attacker can't generate their own
	// challenges, since the hash includes a hash of the instance's private key
	// - an attacker can't steal the skin mid-verification and register the
	// account themselves, since the hash incorporates a token known only to
	// the verifying browser
	challengeBytes := bytes.Join([][]byte{
		[]byte(username),
		app.KeyB3Sum512,
		[]byte(token),
	}, []byte{})

	sum := blake3.Sum512(challengeBytes)
	return sum[:]
}

// GET /challenge-skin
func FrontChallengeSkin(app *App) func(c echo.Context) error {
	type challengeSkinContext struct {
		App                  *App
		User                 *User
		URL                  string
		SuccessMessage       string
		WarningMessage       string
		ErrorMessage         string
		Username             string
		RegistrationProvider string
		SkinBase64           string
		SkinFilename         string
		ChallengeToken       string
		InviteCode           string
	}

	verification_skin_path := path.Join(app.Config.DataDirectory, "assets", "verification-skin.png")
	verification_skin_file := Unwrap(os.Open(verification_skin_path))

	verification_rgba := Unwrap(png.Decode(verification_skin_file))

	verification_img, ok := verification_rgba.(*image.NRGBA)
	if !ok {
		log.Fatal("Invalid verification skin!")
	}

	return withBrowserAuthentication(app, false, func(c echo.Context, user *User) error {
		returnURL := getReturnURL(app, &c)

		username := c.QueryParam("username")
		if err := app.ValidateUsername(username); err != nil {
			setErrorMessage(&c, fmt.Sprintf("Invalid username: %s", err))
			return c.Redirect(http.StatusSeeOther, returnURL)
		}

		inviteCode := c.QueryParam("inviteCode")

		var challengeToken string
		cookie, err := c.Cookie("challengeToken")
		if err != nil || cookie.Value == "" {
			challengeToken, err = RandomHex(32)
			if err != nil {
				return err
			}
			c.SetCookie(&http.Cookie{
				Name:     "challengeToken",
				Value:    challengeToken,
				MaxAge:   BROWSER_TOKEN_AGE_SEC,
				Path:     "/",
				SameSite: http.SameSiteStrictMode,
				HttpOnly: true,
			})
		} else {
			challengeToken = cookie.Value
		}

		// challenge is a 512-bit, 64 byte checksum
		challenge := getChallenge(app, username, challengeToken)

		// Embed the challenge into a skin
		skinSize := 64
		img := image.NewNRGBA(image.Rectangle{image.Point{0, 0}, image.Point{skinSize, skinSize}})

		challengeByte := 0
		for y := 0; y < skinSize; y += 1 {
			for x := 0; x < skinSize; x += 1 {
				var col color.NRGBA
				if SKIN_WINDOW_Y_MIN <= y && y < SKIN_WINDOW_Y_MAX && SKIN_WINDOW_X_MIN <= x && x < SKIN_WINDOW_X_MAX {
					col = color.NRGBA{
						challenge[challengeByte],
						challenge[challengeByte+1],
						challenge[challengeByte+2],
						challenge[challengeByte+3],
					}
					challengeByte += 4
				} else {
					col = verification_img.At(x, y).(color.NRGBA)
				}
				img.SetNRGBA(x, y, col)
			}
		}

		var imgBuffer bytes.Buffer
		err = png.Encode(&imgBuffer, img)
		if err != nil {
			return err
		}

		skinBase64 := base64.StdEncoding.EncodeToString(imgBuffer.Bytes())
		return c.Render(http.StatusOK, "challenge-skin", challengeSkinContext{
			App:            app,
			User:           user,
			URL:            c.Request().URL.RequestURI(),
			SuccessMessage: lastSuccessMessage(&c),
			WarningMessage: lastWarningMessage(&c),
			ErrorMessage:   lastErrorMessage(&c),
			Username:       username,
			SkinBase64:     skinBase64,
			SkinFilename:   username + "-challenge.png",
			ChallengeToken: challengeToken,
			InviteCode:     inviteCode,
		})
	})
}

// type registrationUsernameToIDResponse struct {
// 	Name string `json:"name"`
// 	ID   string `json:"id"`
// }

type proxiedAccountDetails struct {
	Username string
	UUID     string
}

func (app *App) ValidateChallenge(username string, challengeToken *string) (*proxiedAccountDetails, error) {
	base, err := url.Parse(app.Config.RegistrationExistingPlayer.AccountURL)
	if err != nil {
		return nil, err
	}
	base.Path, err = url.JoinPath(base.Path, "users/profiles/minecraft/"+username)
	if err != nil {
		return nil, err
	}

	res, err := MakeHTTPClient().Get(base.String())
	if err != nil {
		log.Printf("Couldn't access registration server at %s: %s\n", base.String(), err)
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		log.Printf("Request to registration server at %s resulted in status code %d\n", base.String(), res.StatusCode)
		return nil, errors.New("registration server returned error")
	}

	var idRes playerNameToUUIDResponse
	err = json.NewDecoder(res.Body).Decode(&idRes)
	if err != nil {
		return nil, err
	}

	base, err = url.Parse(app.Config.RegistrationExistingPlayer.SessionURL)
	if err != nil {
		return nil, fmt.Errorf("Invalid SessionURL %s: %s", app.Config.RegistrationExistingPlayer.SessionURL, err)
	}
	base.Path, err = url.JoinPath(base.Path, "session/minecraft/profile/"+idRes.ID)
	if err != nil {
		return nil, err
	}

	res, err = MakeHTTPClient().Get(base.String())
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		log.Printf("Request to registration server at %s resulted in status code %d\n", base.String(), res.StatusCode)
		return nil, errors.New("registration server returned error")
	}

	var profileRes SessionProfileResponse
	err = json.NewDecoder(res.Body).Decode(&profileRes)
	if err != nil {
		return nil, err
	}
	id := profileRes.ID
	accountUUID, err := IDToUUID(id)
	if err != nil {
		return nil, err
	}

	details := proxiedAccountDetails{
		Username: profileRes.Name,
		UUID:     accountUUID,
	}
	if !app.Config.RegistrationExistingPlayer.RequireSkinVerification {
		return &details, nil
	}

	for _, property := range profileRes.Properties {
		if property.Name == "textures" {
			textureJSON, err := base64.StdEncoding.DecodeString(property.Value)
			if err != nil {
				return nil, err
			}

			var texture texturesValue
			err = json.Unmarshal(textureJSON, &texture)
			if err != nil {
				return nil, err
			}

			if texture.Textures.Skin == nil {
				return nil, errors.New("player does not have a skin")
			}
			res, err = MakeHTTPClient().Get(texture.Textures.Skin.URL)
			if err != nil {
				return nil, err
			}
			defer res.Body.Close()

			rgba_img, err := png.Decode(res.Body)
			if err != nil {
				return nil, err
			}
			img, ok := rgba_img.(*image.NRGBA)
			if !ok {
				return nil, errors.New("invalid image")
			}

			challenge := make([]byte, 64)
			challengeByte := 0
			for y := SKIN_WINDOW_Y_MIN; y < SKIN_WINDOW_Y_MAX; y += 1 {
				for x := SKIN_WINDOW_X_MIN; x < SKIN_WINDOW_X_MAX; x += 1 {
					c := img.NRGBAAt(x, y)
					challenge[challengeByte] = c.R
					challenge[challengeByte+1] = c.G
					challenge[challengeByte+2] = c.B
					challenge[challengeByte+3] = c.A

					challengeByte += 4
				}
			}

			if challengeToken == nil {
				return nil, errors.New("missing challenge token")
			}
			correctChallenge := getChallenge(app, username, *challengeToken)

			if !bytes.Equal(challenge, correctChallenge) {
				return nil, errors.New("skin does not match")
			}

			if err != nil {
				return nil, err
			}

			return &details, nil
		}
	}

	return nil, errors.New("registration server didn't return textures")
}

// POST /register
func FrontRegister(app *App) func(c echo.Context) error {
	returnURL := Unwrap(url.JoinPath(app.FrontEndURL, "web/profile"))
	return func(c echo.Context) error {
		username := c.FormValue("username")
		honeypot := c.FormValue("email")
		password := c.FormValue("password")
		chosenUUID := nilIfEmpty(c.FormValue("uuid"))
		existingPlayer := c.FormValue("existingPlayer") == "on"
		challengeToken := nilIfEmpty(c.FormValue("challengeToken"))
		inviteCode := nilIfEmpty(c.FormValue("inviteCode"))

		failureURL := getReturnURL(app, &c)
		noInviteFailureURL, err := StripQueryParam(failureURL, "invite")
		if err != nil {
			return err
		}

		if honeypot != "" {
			setErrorMessage(&c, "You are now covered in bee stings.")
			return c.Redirect(http.StatusSeeOther, failureURL)
		}

		user, err := app.CreateUser(
			nil, // caller
			username,
			password,
			false, // isAdmin
			false, // isLocked
			chosenUUID,
			existingPlayer,
			challengeToken,
			inviteCode,
			nil, // playerName
			nil, // fallbackPlayer
			nil, // preferredLanguage,
			nil, // skinModel,
			nil, // skinReader,
			nil, // skinURL
			nil, // capeReader,
			nil, // capeURL,
		)
		if err != nil {
			setErrorMessage(&c, err.Error())
			if err == InviteNotFoundError || err == InviteMissingError {
				return c.Redirect(http.StatusSeeOther, noInviteFailureURL)
			}
			return c.Redirect(http.StatusSeeOther, failureURL)
		}

		browserToken, err := RandomHex(32)
		if err != nil {
			return err
		}
		user.BrowserToken = MakeNullString(&browserToken)
		result := app.DB.Save(&user)
		if result.Error != nil {
			return result.Error
		}

		c.SetCookie(&http.Cookie{
			Name:     "browserToken",
			Value:    browserToken,
			MaxAge:   BROWSER_TOKEN_AGE_SEC,
			Path:     "/",
			SameSite: http.SameSiteStrictMode,
			HttpOnly: true,
		})

		return c.Redirect(http.StatusSeeOther, returnURL)
	}
}

// POST /login
func FrontLogin(app *App) func(c echo.Context) error {
	returnURL := app.FrontEndURL + "/web/profile"
	return func(c echo.Context) error {
		failureURL := getReturnURL(app, &c)

		username := c.FormValue("username")
		password := c.FormValue("password")

		if app.TransientLoginEligible(username) {
			setErrorMessage(&c, "Transient accounts cannot access the web interface.")
			return c.Redirect(http.StatusSeeOther, failureURL)
		}

		var user User
		result := app.DB.First(&user, "username = ?", username)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				setErrorMessage(&c, "User not found!")
				return c.Redirect(http.StatusSeeOther, failureURL)
			}
			return result.Error
		}

		if user.IsLocked {
			setErrorMessage(&c, "Account is locked.")
			return c.Redirect(http.StatusSeeOther, failureURL)
		}

		passwordHash, err := HashPassword(password, user.PasswordSalt)
		if err != nil {
			return err
		}

		if !bytes.Equal(passwordHash, user.PasswordHash) {
			setErrorMessage(&c, "Incorrect password!")
			return c.Redirect(http.StatusSeeOther, failureURL)
		}

		browserToken, err := RandomHex(32)
		if err != nil {
			return err
		}

		c.SetCookie(&http.Cookie{
			Name:     "browserToken",
			Value:    browserToken,
			MaxAge:   BROWSER_TOKEN_AGE_SEC,
			Path:     "/",
			SameSite: http.SameSiteStrictMode,
			HttpOnly: true,
		})

		user.BrowserToken = MakeNullString(&browserToken)
		app.DB.Save(&user)

		return c.Redirect(http.StatusSeeOther, returnURL)
	}
}

// POST /delete-user
func FrontDeleteUser(app *App) func(c echo.Context) error {
	return withBrowserAuthentication(app, true, func(c echo.Context, user *User) error {
		returnURL := getReturnURL(app, &c)

		var targetUser *User
		targetUsername := c.FormValue("username")
		if targetUsername == "" || targetUsername == user.Username {
			targetUser = user
		} else {
			if !user.IsAdmin {
				setErrorMessage(&c, "You are not an admin.")
				return c.Redirect(http.StatusSeeOther, app.FrontEndURL)
			}
			var targetUserStruct User
			result := app.DB.First(&targetUserStruct, "username = ?", targetUsername)
			targetUser = &targetUserStruct
			if result.Error != nil {
				setErrorMessage(&c, "User not found.")
				return c.Redirect(http.StatusSeeOther, returnURL)
			}
		}

		err := app.DeleteUser(targetUser)
		if err != nil {
			return err
		}

		if targetUser == user {
			c.SetCookie(&http.Cookie{
				Name:     "browserToken",
				Value:    "",
				MaxAge:   -1,
				Path:     "/",
				SameSite: http.SameSiteStrictMode,
				HttpOnly: true,
			})
		}
		setSuccessMessage(&c, "Account deleted")

		return c.Redirect(http.StatusSeeOther, returnURL)
	})
}
