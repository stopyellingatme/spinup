package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/services"
	"github.com/stopyellingatme/spinup/internal/services/compose"
)

// ocrService is the sidecar service that withService declares in spinup.toml.
var ocrService = services.Service{
	Name: "ocr", Port: 9000, DevPort: 8101, Health: "/health/ready",
	Secrets: []string{"API_TOKEN"}, Env: map[string]string{"MODEL": "small"},
}

// ocrRecord is the release.json record of ocrService with the ssh image ref.
var ocrRecord = ReleaseService{Name: "ocr", Port: 9000, Health: "/health/ready", Image: "todo-ocr:" + testRelease}

// noServiceEnv is the exact .env file of the first ssh deploy of the API-only fixture.
const noServiceEnv = "# Written by spinup deploy. It holds no secret.\n" +
	"SPINUP_COLOR=blue\nAPI_IMAGE=todo-api:" + testRelease + "\n" +
	"PUBLIC_ORIGIN=https://todo.example.com\nPUBLIC_HOST=todo.example.com\nSPINUP_BEHAVIOR_VERSION=3\n" +
	"DEPLOY_UID=1000\nDEPLOY_GID=1001\nAPI_REPLICAS=2\nAPI_CPUS=0.5\nAPI_MEMORY=256m\n" +
	"POSTGRES_CPUS=1.0\nPOSTGRES_MEMORY=1g\n"

// withService declares the service ocr in spinup.toml, adds its Dockerfile, and gives it
// replicas and resources in deploy/environments/staging.toml. jobs turns the jobs module on.
func withService(t *testing.T, x *fx, jobs bool) {
	t.Helper()
	path := filepath.Join(x.root, "spinup.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	toml := string(data)
	if jobs {
		toml = strings.Replace(toml, "behavior_version = 3\n", "behavior_version = 3\nmodules = [\"jobs\"]\n", 1)
	}
	toml += "\n[[services]]\nname = \"ocr\"\nport = 9000\ndev_port = 8101\nhealth = \"/health/ready\"\nsecrets = [\"API_TOKEN\"]\nenv = { MODEL = \"small\" }\n"
	writeTree(t, x.root, map[string]string{
		"spinup.toml":                      toml,
		"services/ocr/Dockerfile":          "FROM scratch\n",
		"deploy/environments/staging.toml": "smoke_urls = [\"/\"]\n[replicas]\napi = 2\nocr = 2\n[resources.api]\ncpus = \"0.5\"\nmemory = \"256m\"\n[resources.ocr]\nmemory = \"1g\"\n",
	})
	x.svc = true
}

// expectFirstDeploy scripts a complete first deploy (no live color, blue next).
// afterGit scripts the local commands of --skip-build. transport scripts the transport step.
func (x *fx) expectFirstDeploy(afterGit, transport func()) {
	x.expectLocalGit()
	if afterGit != nil {
		afterGit()
	}
	x.expectConnect()
	x.expectLock()
	x.expectBegin()
	x.journal(1) // hooks
	x.expectReadCurrent("")
	x.journal(1)
	x.journal(1) // transport
	transport()
	x.journal(1)
	x.journal(1) // upload
	x.expectUploadRelease("")
	x.journal(1)
	x.journal(1) // edge
	x.expectEdge("", "")
	x.journal(1)
	x.journal(1) // migrate
	x.expectMigrate()
	x.journal(1)
	x.journal(1) // switch
	x.expectSwitch(string(PlaceholderUpstream), Blue, false)
	x.journal(1)
	x.journal(1) // smoke
	x.expectSmoke(Blue)
	x.journal(1)
	x.journal(1) // record
	x.expectRecord("", testRelease+"\n")
	x.journal(1)
	x.journal(4) // monitoring (skipped), then post
	x.expectUnlockAndClose()
}

// expectSSHTransport scripts docker save and docker load of refs. The image IDs are sha256:id<N>.
func (x *fx) expectSSHTransport(refs ...string) {
	var local, remote strings.Builder
	for i := range refs {
		fmt.Fprintf(&local, "sha256:id%d linux/amd64 [\"sha256:l%d\"]\n", i, i)
		fmt.Fprintf(&remote, "linux/amd64 [\"sha256:l%d\"]\n", i)
	}
	x.exec.Expect("docker", append([]string{"image", "inspect", "--format", "{{.Id}} " + imageFingerprint}, refs...)...).Return(local.String(), "", 0)
	x.vm.Expect(append([]string{"docker", "image", "inspect", "--format", imageFingerprint}, refs...)...).Return("", "No such image", 1)
	x.exec.Expect("docker", "save", "-o").Return("", "", 0)
	x.ok("docker", "load")
	x.vm.Expect(append([]string{"docker", "image", "inspect", "--format", imageFingerprint}, refs...)...).Return(remote.String(), "", 0)
}

// savedRefs returns the image refs of the docker save call.
func (x *fx) savedRefs() []string {
	for _, c := range x.exec.Calls() {
		if c.Name == "docker" && len(c.Args) > 2 && c.Args[0] == "save" {
			return c.Args[3:]
		}
	}
	return nil
}

func (x *fx) release() Release {
	x.t.Helper()
	var rel Release
	if err := json.Unmarshal(x.upload(testRelDir+"/release.json"), &rel); err != nil {
		x.t.Fatal(err)
	}
	return rel
}

func (x *fx) uploaded(path string) bool {
	for _, u := range x.vm.Uploads() {
		if u.Path == path {
			return true
		}
	}
	return false
}

// TestFlowServicesSSHFirstDeploy deploys an app with one sidecar service over ssh.
func TestFlowServicesSSHFirstDeploy(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	withService(t, x, true)
	api, ocr := "todo-api:"+testRelease, "todo-ocr:"+testRelease
	x.expectFirstDeploy(nil, func() { x.expectSSHTransport(api, ocr) })

	if err := x.run(); err != nil {
		t.Fatalf("deploy: %v\n%s", err, x.out.String())
	}
	x.vm.AssertDone(t)
	x.exec.AssertDone(t)
	if got := x.journalEvents(); !reflect.DeepEqual(got, allSteps) {
		t.Fatalf("journal = %v", got)
	}
	// The build gets every target and the declared services.
	if len(x.built) != 1 || !reflect.DeepEqual(x.built[0].Targets, []string{"api", "ocr"}) || len(x.built[0].Services) != 1 || x.built[0].Services[0].Name != "ocr" {
		t.Fatalf("build = %#v", x.built)
	}
	// The transport moves both images in one archive.
	if got := x.savedRefs(); !reflect.DeepEqual(got, []string{api, ocr}) {
		t.Fatalf("docker save refs = %v", got)
	}
	// compose.services.yml is the output of the renderer for this release.
	want, err := compose.Deploy(compose.DeployInput{
		App: "todo", Services: []services.Service{ocrService}, Images: map[string]string{"ocr": ocr},
		Worker: true, Replicas: map[string]int{"ocr": 2}, Resources: map[string]compose.Resources{"ocr": {Memory: "1g"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(x.upload(testRelDir + "/" + ServicesComposeFile))
	if got != string(want) {
		t.Fatalf("compose.services.yml =\n%s\nwant\n%s", got, want)
	}
	for _, s := range []string{"image: " + ocr, "replicas: 2", "memory: 1g", "\n  worker:\n", "SERVICE_OCR_URL", "/opt/todo/secrets/API_TOKEN:/run/secrets/API_TOKEN:ro"} {
		if !strings.Contains(got, s) {
			t.Errorf("compose.services.yml misses %q", s)
		}
	}
	if strings.Contains(got, "OTEL_") {
		t.Errorf("compose.services.yml has OTel variables without the monitoring module:\n%s", got)
	}
	// The .env file of api and web does not change.
	if env := string(x.upload(testRelDir + "/.env")); env != noServiceEnv {
		t.Fatalf(".env =\n%s\nwant\n%s", env, noServiceEnv)
	}
	rel := x.release()
	if !reflect.DeepEqual(rel.Services, []ReleaseService{ocrRecord}) {
		t.Fatalf("release services = %#v", rel.Services)
	}
	if len(rel.Images) != 2 || rel.Images[1].Service != "ocr" || rel.Images[1].Ref != ocr || rel.Images[1].ID != "sha256:id1" {
		t.Fatalf("release images = %#v", rel.Images)
	}
	var history []ReleaseEntry
	if err := json.Unmarshal(x.upload(testRoot+"/releases.json"), &history); err != nil || len(history[0].Images) != 2 {
		t.Fatalf("releases.json = %#v, %v", history, err)
	}
	x.assertNoSecrets()
}

// TestFlowServicesRegistry deploys a full app with a sidecar service through a registry.
func TestFlowServicesRegistry(t *testing.T) {
	t.Parallel()
	x := newFx(t, true, "registry")
	withService(t, x, false)
	ocr := "ghcr.io/acme/todo-ocr@" + digestOCR
	x.expectFirstDeploy(nil, func() {
		x.ok("docker", "pull", "--quiet", "ghcr.io/acme/todo-api@"+digestAPI)
		x.ok("docker", "pull", "--quiet", "ghcr.io/acme/todo-web@"+digestWeb)
		x.ok("docker", "pull", "--quiet", ocr)
	})
	if err := x.run(); err != nil {
		t.Fatalf("deploy: %v\n%s", err, x.out.String())
	}
	x.vm.AssertDone(t)
	x.exec.AssertDone(t)
	if !x.built[0].Push || !reflect.DeepEqual(x.built[0].Targets, []string{"api", "web", "ocr"}) {
		t.Fatalf("build = %#v", x.built)
	}
	file := string(x.upload(testRelDir + "/" + ServicesComposeFile))
	if !strings.Contains(file, "image: "+ocr) || strings.Contains(file, "\n  worker:\n") {
		t.Fatalf("compose.services.yml =\n%s", file)
	}
	rel := x.release()
	if len(rel.Services) != 1 || rel.Services[0].Image != ocr || rel.Transport != TransportRegistry {
		t.Fatalf("release = %#v", rel)
	}
	env := string(x.upload(testRelDir + "/.env"))
	if strings.Contains(env, "OCR") || !strings.Contains(env, "WEB_IMAGE=ghcr.io/acme/todo-web@"+digestWeb+"\n") {
		t.Fatalf(".env =\n%s", env)
	}
	x.assertNoSecrets()
}

// TestFlowServicesSkipBuild uses the existing service images of the release ID.
func TestFlowServicesSkipBuild(t *testing.T) {
	t.Parallel()
	t.Run("ssh latest tag", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, false, "")
		withService(t, x, false)
		x.o.SkipBuild = true
		api, ocr := "todo-api:"+testRelease, "todo-ocr:"+testRelease
		x.expectFirstDeploy(func() {
			x.exec.Expect("docker", "image", "ls", "--format", "{{.Tag}}", "todo-api").Return("dev\n"+testRelease+"\n", "", 0)
		}, func() { x.expectSSHTransport(api, ocr) })
		if err := x.run(); err != nil {
			t.Fatalf("deploy: %v\n%s", err, x.out.String())
		}
		x.vm.AssertDone(t)
		x.exec.AssertDone(t)
		if len(x.built) != 0 || !reflect.DeepEqual(x.savedRefs(), []string{api, ocr}) {
			t.Fatalf("built %d, saved %v", len(x.built), x.savedRefs())
		}
		if rel := x.release(); !reflect.DeepEqual(rel.Services, []ReleaseService{ocrRecord}) {
			t.Fatalf("release services = %#v", rel.Services)
		}
	})
	t.Run("registry tag", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, false, "registry")
		withService(t, x, false)
		x.o.SkipBuild, x.o.Tag = true, testRelease
		x.expectFirstDeploy(nil, func() {
			for _, name := range []string{"api", "ocr"} {
				ref := "ghcr.io/acme/todo-" + name + ":" + testRelease
				digest := map[string]string{"api": digestAPI, "ocr": digestOCR}[name]
				x.exec.Expect("docker", "buildx", "imagetools", "inspect", "--format", "{{json .Manifest}}", ref).Return(`{"digest":"`+digest+`"}`, "", 0)
				x.ok("docker", "pull", "--quiet", "ghcr.io/acme/todo-"+name+"@"+digest)
			}
		})
		if err := x.run(); err != nil {
			t.Fatalf("deploy: %v\n%s", err, x.out.String())
		}
		x.vm.AssertDone(t)
		x.exec.AssertDone(t)
		if rel := x.release(); len(rel.Services) != 1 || rel.Services[0].Image != "ghcr.io/acme/todo-ocr@"+digestOCR {
			t.Fatalf("release services = %#v", rel.Services)
		}
	})
}

// journalWith returns a journal of testRelease with every step up to and including last
// done. The build and transport steps record images.
func journalWith(t *testing.T, last string, images []Image) string {
	t.Helper()
	data, _ := json.Marshal(images)
	var b strings.Builder
	for _, step := range Steps {
		detail := ""
		if step == "build" || step == "transport" {
			detail = string(data)
		}
		for _, status := range []string{"started", "done"} {
			line, err := json.Marshal(Entry{Release: testRelease, Step: step, Status: status, At: testNow, Detail: detail})
			if err != nil {
				t.Fatal(err)
			}
			b.Write(append(line, '\n'))
		}
		if step == last {
			break
		}
	}
	return b.String()
}

func serviceImages() []Image {
	return []Image{
		{Service: "api", Tag: "todo-api:" + testRelease, Ref: "todo-api:" + testRelease, ID: "sha256:api"},
		{Service: "ocr", Tag: "todo-ocr:" + testRelease, Ref: "todo-ocr:" + testRelease, ID: "sha256:ocr"},
	}
}

// TestFlowServicesHealthFailureRollsBack stops the new color when a service replica is not ready.
func TestFlowServicesHealthFailureRollsBack(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	withService(t, x, false)
	x.o.Resume = true
	x.expectLocalGit()
	x.expectConnect()
	x.expectLock()
	x.vm.Expect("sh", "-c").Return(journalWith(t, "migrate", serviceImages()), "", 0)
	rel, _ := json.Marshal(Release{Release: testRelease, Color: Green, Images: serviceImages(), Services: []ReleaseService{ocrRecord}})
	x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRelDir+"/release.json").Return(string(rel), "", 0)
	x.journal(1)
	x.expectUpstream(string(UpstreamFile(Blue)))
	x.vm.Expect(psArgv("todo", "caddy")...).Return("caddy1\n", "", 0)
	x.ok(x.colorArgv(Green, "up", "-d", "--wait", "--wait-timeout", "4", "--remove-orphans")...)
	// Two readiness attempts. The api is ready. The service is not.
	for range 2 {
		x.vm.Expect(psArgv("todo-green", "api")...).Return("napi1\n", "", 0)
		x.vm.Expect(inspectArgv("napi1")...).Return("10.0.1.1\n", "", 0)
		x.ok(wgetReady("10.0.1.1", 8080, "/readyz")...)
		x.vm.Expect(psArgv("todo-green", "ocr")...).Return("nocr1\n", "", 0)
		x.vm.Expect(inspectArgv("nocr1")...).Return("10.0.1.5\n", "", 0)
		x.vm.Expect(wgetReady("10.0.1.5", 9000, "/health/ready")...).Return("", "server returned error: HTTP/1.1 503", 1)
	}
	x.ok(x.colorArgv(Green, "down", "-t", "15")...)
	x.journal(1)
	x.expectUnlockAndClose()

	err := x.run()
	var health *HealthError
	if !errors.As(err, &health) || !health.Stopped || !strings.Contains(err.Error(), "ocr replica 10.0.1.5 is not ready at /health/ready") {
		t.Fatalf("error = %v", err)
	}
	x.vm.AssertDone(t)
	x.exec.AssertDone(t)
	if got := x.journalEvents(); !reflect.DeepEqual(got, []string{"switch:started", "switch:failed"}) {
		t.Fatalf("journal = %v", got)
	}
	for _, u := range x.vm.Uploads() {
		if strings.HasSuffix(u.Path, "upstream.caddy") {
			t.Fatalf("upstream changed: %s", u.Path)
		}
	}
}

// TestFlowServicesResume continues a release after its transport: the upload renders
// compose.services.yml from the images of the journal.
func TestFlowServicesResume(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	withService(t, x, false)
	x.o.Resume = true
	x.expectLocalGit()
	x.expectConnect()
	x.expectLock()
	x.vm.Expect("sh", "-c").Return(journalWith(t, "transport", serviceImages()), "", 0)
	x.journal(1)
	x.expectUploadRelease("")
	x.journal(1)
	x.journal(1)
	x.expectEdge("", "")
	x.journal(1)
	x.journal(1)
	x.expectMigrate()
	x.journal(1)
	x.journal(1)
	x.expectSwitch(string(PlaceholderUpstream), Blue, false)
	x.journal(1)
	x.journal(1)
	x.expectSmoke(Blue)
	x.journal(1)
	x.journal(1)
	x.expectRecord("", testRelease+"\n")
	x.journal(1)
	x.journal(4)
	x.expectUnlockAndClose()
	if err := x.run(); err != nil {
		t.Fatalf("resume: %v\n%s", err, x.out.String())
	}
	x.vm.AssertDone(t)
	x.exec.AssertDone(t)
	if len(x.built) != 0 {
		t.Fatal("resume built images")
	}
	if file := string(x.upload(testRelDir + "/" + ServicesComposeFile)); !strings.Contains(file, "image: todo-ocr:"+testRelease) {
		t.Fatalf("compose.services.yml =\n%s", file)
	}
	rel := x.release()
	if !reflect.DeepEqual(rel.Services, []ReleaseService{ocrRecord}) || !reflect.DeepEqual(rel.Images, serviceImages()) {
		t.Fatalf("release = %#v", rel)
	}
}

// TestFlowServicesResumeFollowsRecord switches with the services of release.json after
// the upload, also when spinup.toml no longer declares them.
func TestFlowServicesResumeFollowsRecord(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	x.svc = true // spinup.toml has no service. Only the record of the release has one.
	x.o.Resume = true
	x.expectLocalGit()
	x.expectConnect()
	x.expectLock()
	x.vm.Expect("sh", "-c").Return(journalWith(t, "migrate", serviceImages()), "", 0)
	rel, _ := json.Marshal(Release{Release: testRelease, Color: Blue, Images: serviceImages(), Services: []ReleaseService{ocrRecord}})
	x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRelDir+"/release.json").Return(string(rel), "", 0)
	x.journal(1)
	x.expectSwitch(string(PlaceholderUpstream), Blue, false)
	x.journal(1)
	x.journal(1)
	x.expectSmoke(Blue)
	x.journal(1)
	x.journal(1)
	x.expectRecord("", testRelease+"\n")
	x.journal(1)
	x.journal(4)
	x.expectUnlockAndClose()
	if err := x.run(); err != nil {
		t.Fatalf("resume: %v\n%s", err, x.out.String())
	}
	x.vm.AssertDone(t)
	var history []ReleaseEntry
	if err := json.Unmarshal(x.upload(testRoot+"/releases.json"), &history); err != nil || len(history[0].Images) != 2 {
		t.Fatalf("releases.json = %#v, %v", history, err)
	}
}

// TestRollbackToReleaseWithService uses the services and Compose files of the target release.
func TestRollbackToReleaseWithService(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	x.svc = true // The record of the target release, not spinup.toml, selects the service.
	currentID := "20261015T090000Z-abcdef012345"
	current := rollbackSnapshot{Release: Release{Release: currentID, Color: Green, MigrationVersion: "2"}}
	safe := true
	current.MigrationsSafe = &safe
	target := rollbackSnapshot{Release: Release{Release: testRelease, App: "todo", Env: "staging", GitSHA: testSHA, Color: Blue, MigrationVersion: "2", Images: serviceImages(), Services: []ReleaseService{ocrRecord}}}
	currentJSON, _ := json.Marshal(current)
	targetJSON, _ := json.Marshal(target)
	history, _ := json.Marshal([]ReleaseEntry{{Release: currentID}, {Release: testRelease}})
	x.expectLock()
	rollbackStepStart(x)
	x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRoot+"/releases.json").Return(string(history), "", 0)
	x.expectReadCurrent(string(currentJSON))
	x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRelDir+"/release.json").Return(string(targetJSON), "", 0)
	x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRoot+"/releases/"+currentID+"/release.json").Return(string(currentJSON), "", 0)
	x.ok("sh", "-c")
	rollbackStepStart(x)
	x.expectUpstream(string(UpstreamFile(Green)))
	x.ok("sh", "-c", rollbackColorScript, "spinup-rollback", testRelDir, Blue, "todo")
	x.ok("sh", "-c")
	rollbackStepStart(x)
	x.expectSwitch(string(UpstreamFile(Green)), Blue, false)
	x.ok("sh", "-c")
	rollbackStepStart(x)
	x.http["https://todo.example.com/healthz"] = []httpCall{{status: 200}}
	x.http["https://todo.example.com/version"] = []httpCall{{status: 200, body: `{"version":"` + testRelease + `"}`}}
	x.ok("sh", "-c")
	rollbackStepStart(x)
	x.vm.ExpectUpload(testRelDir+"/release.json").Return("", "", 0)
	x.ok("ln", "-sfn", "releases/"+testRelease, testRoot+"/current")
	x.vm.ExpectUpload(testRoot+"/releases.json").Return("", "", 0)
	x.ok("sh", "-c")
	x.expectUnlockAndClose()
	if err := Rollback(context.Background(), RollbackOptions{Options: x.o}, x.d); err != nil {
		t.Fatal(err)
	}
	x.vm.AssertDone(t)
	var rel Release
	if err := json.Unmarshal(x.upload(testRelDir+"/release.json"), &rel); err != nil || !reflect.DeepEqual(rel.Services, []ReleaseService{ocrRecord}) || rel.Color != Blue {
		t.Fatalf("release.json = %#v, %v", rel, err)
	}
}

// TestFlowNoServicesUnchanged checks that an app without services deploys as before,
// also with a stray deploy/compose.services.yml: the same files, .env, and Compose argv.
func TestFlowNoServicesUnchanged(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	writeTree(t, x.root, map[string]string{"deploy/compose.services.yml": "services: {}\n"})
	x.expectFirstDeploy(nil, func() { x.expectSSHTransport("todo-api:" + testRelease) })
	if err := x.run(); err != nil {
		t.Fatalf("deploy: %v\n%s", err, x.out.String())
	}
	x.vm.AssertDone(t)
	x.exec.AssertDone(t)
	if !reflect.DeepEqual(x.built[0].Targets, []string{"api"}) || len(x.built[0].Services) != 0 {
		t.Fatalf("build = %#v", x.built)
	}
	if env := string(x.upload(testRelDir + "/.env")); env != noServiceEnv {
		t.Fatalf(".env =\n%s\nwant\n%s", env, noServiceEnv)
	}
	if x.uploaded(testRelDir + "/" + ServicesComposeFile) {
		t.Fatal("a release without services got compose.services.yml")
	}
	if raw := string(x.upload(testRelDir + "/release.json")); strings.Contains(raw, `"services"`) {
		t.Fatalf("release.json records services:\n%s", raw)
	}
	for _, c := range x.vm.Calls() {
		if strings.Contains(strings.Join(c.Argv, " "), ServicesComposeFile) {
			t.Fatalf("command uses %s: %v", ServicesComposeFile, c.Argv)
		}
	}
}

// TestFlowServicesPlan lists the service image and compose.services.yml without a change.
func TestFlowServicesPlan(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	withService(t, x, false)
	x.o.Plan = true
	x.expectLocalGit()
	x.expectConnect()
	x.expectUpstream("")
	x.expectReadCurrent("")
	x.vm.Expect("sh", "-c", readCurrentScript, "spinup-current", testRoot+"/current").Return("", "", 0)
	x.vm.Expect("sh", "-c").Return("", "", 3) // no lock
	x.vm.ExpectClose().Return("", "", 0)
	if err := x.run(); err != nil {
		t.Fatalf("plan: %v", err)
	}
	x.vm.AssertDone(t)
	out := x.out.String()
	for _, want := range []string{"  ocr: none -> todo-ocr:" + testRelease, "  added " + ServicesComposeFile} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output misses %q:\n%s", want, out)
		}
	}
	if len(x.vm.Uploads()) != 0 || len(x.built) != 0 {
		t.Fatal("--plan changed something")
	}
}

func TestColorServicesAndFiles(t *testing.T) {
	t.Parallel()
	got := ColorServices(true, []ReleaseService{ocrRecord, {Name: "tts", Port: 7000, Health: "/healthz"}})
	want := []Service{{"api", 8080, "/readyz"}, {"web", 3000, "/healthz"}, {"ocr", 9000, "/health/ready"}, {"tts", 7000, "/healthz"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ColorServices = %v", got)
	}
	if got := ColorServices(false, nil); !reflect.DeepEqual(got, want[:1]) {
		t.Fatalf("ColorServices(false, nil) = %v", got)
	}
	if got := ColorComposeFiles(nil); !reflect.DeepEqual(got, []string{"compose.yml"}) {
		t.Fatalf("files = %v", got)
	}
	if got := ColorComposeFiles([]ReleaseService{ocrRecord}); !reflect.DeepEqual(got, []string{"compose.yml", ServicesComposeFile}) {
		t.Fatalf("files = %v", got)
	}
	files := []releaseFile{{Path: "compose.yml"}, {Path: "compose.edge.yml"}, {Path: ServicesComposeFile}, {Path: "compose.backups.yml"}}
	if got := edgeComposeFiles(files); !reflect.DeepEqual(got, []string{"compose.edge.yml", "compose.backups.yml"}) {
		t.Fatalf("edge files = %v", got)
	}
	for _, bad := range []string{"../compose.yml", "x/compose.yml", "", ".env"} {
		err := Switch(context.Background(), nil, SwitchOptions{App: "todo", ReleaseDir: testRelDir, Color: Blue, ComposeFiles: []string{bad}})
		if err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("Switch with Compose file %q: %v", bad, err)
		}
	}
}

func TestServicesComposeInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		modules     bool
		replicas    int
		wantReplica string
	}{
		{"zero replicas gives one", false, 0, "replicas: 1"},
		{"monitoring and jobs", true, 3, "replicas: 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := &local{app: "todo", sidecars: []services.Service{ocrService}, jobs: tt.modules, otel: tt.modules,
				envFile: EnvFile{Replicas: map[string]int{"ocr": tt.replicas}, Resources: map[string]ResourceLimits{"ocr": {CPUs: "2"}}}}
			f := &flow{l: l, images: serviceImages()}
			data, err := f.servicesCompose()
			if err != nil {
				t.Fatal(err)
			}
			s := string(data)
			if !strings.Contains(s, tt.wantReplica) || !strings.Contains(s, `cpus: "2"`) {
				t.Fatalf("compose.services.yml =\n%s", s)
			}
			if has := strings.Contains(s, `OTEL_SERVICE_NAME: "todo-ocr"`); has != tt.modules {
				t.Errorf("OTel variables present = %v", has)
			}
			if has := strings.Contains(s, "\n  worker:\n"); has != tt.modules {
				t.Errorf("worker present = %v", has)
			}
		})
	}
	// A service without an image ref fails with a Try line.
	f := &flow{l: &local{app: "todo", sidecars: []services.Service{ocrService}}}
	if _, err := f.servicesCompose(); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("error = %v", err)
	}
	// An app without services gets no file.
	if data, err := (&flow{l: &local{app: "todo"}}).servicesCompose(); data != nil || err != nil {
		t.Fatalf("no services = %q, %v", data, err)
	}
}
