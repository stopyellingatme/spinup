package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor/composelint"
	"github.com/stopyellingatme/spinup/internal/services"
)

func fullService() services.Service {
	return services.Service{
		Name: "ocr", Port: 8080, DevPort: 8101, Health: "/healthz", OpenAPI: "openapi.yaml",
		Healthcheck: []string{"/service", "healthcheck"},
		Secrets:     []string{"OCR_LICENSE_KEY", "OCR_SALT"},
		Env:         map[string]string{"MODEL": "small", "BANNER": "cost $5"},
		DB:          true, GPUs: "all",
	}
}

func minimalService(name string, port, dev int) services.Service {
	return services.Service{Name: name, Port: port, DevPort: dev, Health: "/healthz"}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if os.Getenv("SPINUP_GOLDEN_UPDATE") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v. Try: SPINUP_GOLDEN_UPDATE=1 go test ./internal/services/compose", err)
	}
	if string(want) != string(got) {
		t.Errorf("output differs from %s.\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func TestDeployGolden(t *testing.T) {
	t.Parallel()
	images := map[string]string{"ocr": "todo-ocr:r1", "tts": "todo-tts:r1"}
	tests := []struct {
		name string
		in   DeployInput
	}{
		{"deploy_full", DeployInput{
			App: "todo", Services: []services.Service{fullService()}, Images: images,
			Monitoring: true, Worker: true,
			Replicas:  map[string]int{"ocr": 2},
			Resources: map[string]Resources{"ocr": {CPUs: "1.5", Memory: "2g"}},
		}},
		{"deploy_minimal", DeployInput{
			App: "todo", Services: []services.Service{minimalService("ocr", 8080, 8101)}, Images: images,
		}},
		{"deploy_two", DeployInput{
			App: "todo", Worker: true, Monitoring: true,
			Services: []services.Service{minimalService("ocr", 8080, 8101), minimalService("tts", 9000, 8102)},
			Images:   images, Replicas: map[string]int{"tts": 3},
			Resources: map[string]Resources{"tts": {Memory: "1g"}},
		}},
		{"deploy_gpu_count", DeployInput{
			App: "todo", Images: images,
			Services: []services.Service{{Name: "ocr", Port: 8080, DevPort: 8101, Health: "/healthz", GPUs: "2"}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Deploy(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, tt.name, got)
			again, _ := Deploy(tt.in)
			if string(again) != string(got) {
				t.Error("Deploy is not deterministic")
			}
		})
	}
}

func TestDevGolden(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   DevInput
	}{
		{"dev_full", DevInput{App: "todo", Services: []services.Service{fullService()}, LGTM: true}},
		{"dev_two", DevInput{Services: []services.Service{minimalService("ocr", 8080, 8101), minimalService("tts", 9000, 8102)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Dev(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, tt.name, got)
		})
	}
}

func TestWorkerAndMonitoringSwitches(t *testing.T) {
	t.Parallel()
	for _, worker := range []bool{false, true} {
		for _, monitoring := range []bool{false, true} {
			got, err := Deploy(DeployInput{
				App: "todo", Services: []services.Service{minimalService("ocr", 8080, 8101)},
				Images: map[string]string{"ocr": "todo-ocr:r1"}, Worker: worker, Monitoring: monitoring,
			})
			if err != nil {
				t.Fatal(err)
			}
			s := string(got)
			if has := strings.Contains(s, "\n  worker:\n"); has != worker {
				t.Errorf("worker=%v: worker service present = %v", worker, has)
			}
			if has := strings.Contains(s, "OTEL_SERVICE_NAME: \"todo-ocr\""); has != monitoring {
				t.Errorf("monitoring=%v: OTel variables present = %v", monitoring, has)
			}
			if !strings.Contains(s, "\n  api:\n    environment:\n      SERVICE_OCR_URL: \"http://ocr-${SPINUP_COLOR}:8080\"") {
				t.Errorf("api has no SERVICE_OCR_URL:\n%s", s)
			}
			if strings.Contains(s, "\nnetworks:") {
				t.Error("the file must define no top-level networks")
			}
		}
	}
}

func TestEmptyServices(t *testing.T) {
	t.Parallel()
	if got, err := Deploy(DeployInput{App: "todo"}); got != nil || err != nil {
		t.Errorf("Deploy = %q, %v; want nil, nil", got, err)
	}
	if got, err := Dev(DevInput{}); got != nil || err != nil {
		t.Errorf("Dev = %q, %v; want nil, nil", got, err)
	}
}

func TestDeployErrors(t *testing.T) {
	t.Parallel()
	svc := []services.Service{minimalService("ocr", 8080, 8101)}
	tests := []struct {
		name string
		in   DeployInput
		want string
	}{
		{"no image", DeployInput{App: "todo", Services: svc}, "no image ref"},
		{"no app", DeployInput{Services: svc, Images: map[string]string{"ocr": "x:1"}}, "app name is empty"},
		{"reserved name", DeployInput{App: "todo", Services: []services.Service{minimalService("api", 1, 2)}, Images: map[string]string{"api": "x:1"}}, "reserved"},
		{"negative replicas", DeployInput{App: "todo", Services: svc, Images: map[string]string{"ocr": "x:1"}, Replicas: map[string]int{"ocr": -1}}, "replicas"},
		{"PORT in env", DeployInput{App: "todo", Services: []services.Service{{Name: "ocr", Port: 1, Env: map[string]string{"PORT": "1"}}}, Images: map[string]string{"ocr": "x:1"}}, "twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Deploy(tt.in)
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "Try:") {
				t.Errorf("error = %v; want it to contain %q and Try:", err, tt.want)
			}
		})
	}
}

func TestComposeSecurityLint(t *testing.T) {
	t.Parallel()
	in := DeployInput{
		App: "todo", Services: []services.Service{fullService(), minimalService("tts", 9000, 8102)},
		Images:     map[string]string{"ocr": "todo-ocr:r1", "tts": "todo-tts:r1"},
		Monitoring: true, Worker: true,
	}
	deploy, err := Deploy(in)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := Dev(DevInput{Services: in.Services, LGTM: true})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"compose.services.yml": deploy, "compose.dev.services.yml": dev} {
		findings, err := composelint.Lint(name, data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, f := range findings {
			t.Errorf("%s: %s %s: %s", name, f.Service, f.Rule, f.Message)
		}
	}
}
