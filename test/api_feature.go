package test

import (
	"encoding/json"
	"fmt"
	"github.com/DATA-DOG/godog/gherkin"
	"github.com/iridiumdev/gin-jwt"
	"github.com/onsi/gomega"
	"gopkg.in/resty.v1"
)

type ApiFeature struct {
	resp           *resty.Response
	jsonSpec       *JSONSpec
	BaseUrl        string
	AuthMiddleware *jwt.GinJWTMiddleware
	accessToken    string
}

func (a *ApiFeature) ResetResponse(interface{}) {
	a.resp = &resty.Response{}
	a.jsonSpec = nil
}

func (a *ApiFeature) IAmLoggedInAs(username string) (err error) {

	token, _, err := a.AuthMiddleware.TokenGenerator(jwt.MapClaims{
		"id":    username,
		"scope": "access",
	})
	a.accessToken = token

	return
}

func (a *ApiFeature) KeepJSONResponseAt(path string, name string) (err error) {
	a.jsonSpec.KeepValue(path, name)

	// handle panic
	defer func() {
		switch t := recover().(type) {
		case string:
			err = fmt.Errorf(t)
		case error:
			err = t
		}
	}()
	return
}

func (a *ApiFeature) IDoARequest(method string, path string) (err error) {
	var resp = &resty.Response{}

	if method == "GET" {
		resp, err = resty.R().
			SetHeader("Authorization", "Bearer "+a.accessToken).
			Get(a.BaseUrl + path)
	} else if method == "DELETE" {
		resp, err = resty.R().
			SetHeader("Authorization", "Bearer "+a.accessToken).
			Delete(a.BaseUrl + path)
	} else {
		return fmt.Errorf("unexpected method type %s, can be either GET or DELETE", method)
	}

	if err != nil {
		return
	}

	a.resp = resp
	a.jsonSpec = NewJSONSpec(string(resp.Body()))

	// handle panic
	defer func() {
		switch t := recover().(type) {
		case string:
			err = fmt.Errorf(t)
		case error:
			err = t
		}
	}()
	return
}

func (a *ApiFeature) IDoARequestWithBody(method string, path string, body *gherkin.DocString) (err error) {

	var resp = &resty.Response{}

	var bodyRaw []byte
	var bodyString interface{}

	// re-encode the body string
	if err = json.Unmarshal([]byte(body.Content), &bodyString); err != nil {
		return
	}
	if bodyRaw, err = json.MarshalIndent(bodyString, "", "  "); err != nil {
		return
	}

	if method == "POST" {
		resp, err = resty.R().
			SetHeader("Content-Type", "application/json").
			SetHeader("Authorization", "Bearer "+a.accessToken).
			SetBody(bodyRaw).
			Post(a.BaseUrl + path)
	} else if method == "PUT" {
		resp, err = resty.R().
			SetHeader("Authorization", "Bearer "+a.accessToken).
			SetBody(bodyRaw).
			Put(a.BaseUrl + path)
	} else {
		return fmt.Errorf("unexpected method type %s, can be either POST or PUT", method)
	}

	if err != nil {
		return
	}

	a.resp = resp
	a.jsonSpec = NewJSONSpec(string(resp.Body()))

	// handle panic
	defer func() {
		switch t := recover().(type) {
		case string:
			err = fmt.Errorf(t)
		case error:
			err = t
		}
	}()
	return
}

func (a *ApiFeature) TheResponseShouldBeAndMatchThisJson(status int, body *gherkin.DocString) (err error) {
	err = a.TheResponseShouldBe(status)
	if err != nil {
		return
	}

	expectedRaw := a.jsonSpec.ReplaceFromMemory(body.Content)

	gomega.Expect(a.resp.Body()).To(gomega.MatchJSON(expectedRaw))

	return
}

func (a *ApiFeature) TheResponseShouldBe(status int) error {
	if status != a.resp.StatusCode() {
		return fmt.Errorf("expected response code to be: %d, but actual is: %d", status, a.resp.StatusCode())
	}
	return nil
}