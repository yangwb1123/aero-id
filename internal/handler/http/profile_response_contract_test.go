package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/service"
)

func TestOpenAPIProfileRequiresRootTextFields(t *testing.T) {
	spec := loadOpenAPISpec(t)
	encoded, err := yaml.Marshal(spec.Components.Schemas["Profile"])
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required   []string `yaml:"required"`
		Properties map[string]struct {
			Type string `yaml:"type"`
		} `yaml:"properties"`
	}
	if err := yaml.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"display_name", "avatar_url", "locale", "timezone"} {
		if !slices.Contains(schema.Required, field) || schema.Properties[field].Type != "string" {
			t.Errorf("Profile must require root string field %s", field)
		}
	}
}

type profileResponseContractService struct {
	service.AccountService
	profile *domain.AccountProfile
}

func (s *profileResponseContractService) GetProfile(context.Context) (*domain.AccountProfile, error) {
	return s.profile, nil
}

func (s *profileResponseContractService) UpdateProfile(context.Context, service.UpdateAccountProfileInput) (*domain.AccountProfile, error) {
	return s.profile, nil
}

func TestProfileHTTPAlwaysReturnsFourRootStrings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	id := uuid.MustParse("01800000-0000-7000-8000-000000000002")
	for _, empty := range []bool{true, false} {
		for _, method := range []string{http.MethodGet, http.MethodPatch} {
			t.Run(method+map[bool]string{true: "/empty", false: "/text"}[empty], func(t *testing.T) {
				profile := &domain.AccountProfile{AccountID: id}
				if !empty {
					profile.DisplayName, profile.AvatarURL = "资料 name", "https://avatar.example.test/picture.png"
					profile.Locale, profile.Timezone = "zh-CN", "UTC"
				}
				svc := &profileResponseContractService{profile: profile}
				engine := gin.New()
				httpapi.NewAccountHandler(svc, nil).RegisterRoutes(httpapi.Routes{ControlPlane: engine.Group("/v1")})
				request := httptest.NewRequest(method, "/v1/me/profile", strings.NewReader("{}"))
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusOK {
					t.Fatalf("Profile HTTP status = %d", recorder.Code)
				}
				assertProfileRootStrings(t, recorder, profile)
			})
		}
	}
}

func assertProfileRootStrings(t *testing.T, recorder *httptest.ResponseRecorder, profile *domain.AccountProfile) {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{"display_name": profile.DisplayName, "avatar_url": profile.AvatarURL,
		"locale": profile.Locale, "timezone": profile.Timezone}
	for field, want := range expected {
		got, ok := envelope.Data[field].(string)
		if !ok || got != want {
			t.Errorf("Profile field %s must be present with its exact text", field)
		}
	}
}
