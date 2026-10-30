package services

import (
	"strings"
	"testing"
)

func valid() Service {
	return Service{Name: "ocr", Port: 8080, DevPort: 8101, Health: "/healthz", OpenAPI: "openapi.yaml"}
}

func TestValidateAcceptsAFullService(t *testing.T) {
	s := valid()
	s.Healthcheck = []string{"/service", "healthcheck"}
	s.Secrets = []string{"OCR_LICENSE_KEY"}
	s.Env = map[string]string{"MODEL": "small"}
	s.DB = true
	s.GPUs = "all"
	two := valid()
	two.Name, two.DevPort, two.OpenAPI, two.GPUs = "embed", 8102, "", "2"
	if err := Validate([]Service{s, two}, nil); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Service)
		want   string
	}{
		{"bad name", func(s *Service) { s.Name = "OCR" }, "must match"},
		{"hyphen", func(s *Service) { s.Name = "ocr-svc" }, "must match"},
		{"reserved", func(s *Service) { s.Name = "worker" }, "reserved"},
		{"go keyword type", func(s *Service) { s.Name = "type" }, "Go keyword"},
		{"go keyword go", func(s *Service) { s.Name = "go" }, "Go keyword"},
		{"go keyword fallthrough", func(s *Service) { s.Name = "fallthrough" }, "Go keyword"},
		{"port", func(s *Service) { s.Port = 0 }, "port 0"},
		{"dev port range", func(s *Service) { s.DevPort = 70000 }, "dev_port 70000"},
		{"dev port reserved", func(s *Service) { s.DevPort = 5173 }, "used by the app"},
		{"health", func(s *Service) { s.Health = "healthz" }, "starts with /"},
		{"openapi escape", func(s *Service) { s.OpenAPI = "../api.yaml" }, "relative path"},
		{"openapi absolute", func(s *Service) { s.OpenAPI = "/x.yaml" }, "relative path"},
		{"secret", func(s *Service) { s.Secrets = []string{"lower"} }, "secret \"lower\""},
		{"env reserved", func(s *Service) { s.Env = map[string]string{"OTEL_X": "1"} }, "OTEL_"},
		{"env pattern", func(s *Service) { s.Env = map[string]string{"a-b": "1"} }, "env key"},
		{"gpus", func(s *Service) { s.GPUs = "0" }, "gpus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := valid()
			tc.change(&s)
			err := Validate([]Service{s}, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("Validate() = %v, want an error with %q and a Try: line", err, tc.want)
			}
		})
	}
}

func TestValidateRejectsDuplicates(t *testing.T) {
	a, b := valid(), valid()
	err := Validate([]Service{a, b}, nil)
	if err == nil || !strings.Contains(err.Error(), "declared twice") || !strings.Contains(err.Error(), "also used by service") {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestDerivedNames(t *testing.T) {
	s := valid()
	for got, want := range map[string]string{
		EnvPrefix("ocr"):         "SERVICE_OCR",
		URLEnv("ocr"):            "SERVICE_OCR_URL",
		DeployURL(s):             "http://ocr-${SPINUP_COLOR}:8080",
		DevURL(s):                "http://127.0.0.1:8101",
		Alias("ocr", "blue"):     "ocr-blue",
		ImageName("todo", "ocr"): "todo-ocr",
		OTelName("todo", "ocr"):  "todo-ocr",
		ContextDir("ocr"):        "services/ocr",
		Dockerfile("ocr"):        "services/ocr/Dockerfile",
		ClientDir("ocr"):         "api/internal/services/ocr",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
