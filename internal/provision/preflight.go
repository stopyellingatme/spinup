package provision

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean"
	"github.com/stopyellingatme/spinup/internal/deploy"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/server"
	"github.com/stopyellingatme/spinup/internal/stackversions"
)

// minMemoryMiB is the smallest droplet memory that runs the stack (ADR 0017 §4).
const minMemoryMiB = 2048

// run is the state of one Up.
type run struct {
	base
	envFile       deploy.EnvFile
	scaffoldedEnv bool
	region        string
	size          string
	image         string
	team          string
	sizeInfo      digitalocean.Size
	pub           string // operator public key
	sshKey        digitalocean.SSHKey
	fw            digitalocean.Firewall
	droplets      []digitalocean.Droplet
	droplet       *digitalocean.Droplet
	ip            string
	hostPub       string
	hostPriv      string // only in memory
	runner        remote.Runner
	// connected is true after the first SSH command worked.
	connected bool
	domain    string
	publicURL string
}

func newRun(o Options, d Deps) *run {
	d.fill()
	return &run{base: base{o: o, d: d}}
}

func defaults() stackversions.DigitalOcean { return stackversions.MustLoad().DigitalOcean }

// preflight checks everything that needs no network write and no cost (ADR 0017 §7 step 1).
func (r *run) preflight(ctx context.Context) error {
	if err := needCloud(r.d); err != nil {
		return err
	}
	if err := r.load(); err != nil {
		return err
	}
	def := defaults()
	if _, err := os.Stat(filepath.Join(r.root, "deploy", "cloud-init", "user-data.yaml")); err != nil {
		return fail("find the deploy pack", "deploy/cloud-init/user-data.yaml does not exist", "add the deploy pack to the app")
	}
	if err := r.loadEnvironmentFile(); err != nil {
		return err
	}
	if err := r.checkTools(ctx); err != nil {
		return err
	}
	if !r.o.Deploy.AllowDirty {
		if err := r.checkClean(ctx); err != nil {
			return err
		}
	}
	r.image = first(r.o.Image, r.st.Image, def.Image)
	if !strings.HasPrefix(r.image, "ubuntu-") {
		return fail("use the image "+r.image, "the cloud-init template supports Ubuntu images only", "pass --image with an ubuntu-* slug such as "+def.Image)
	}
	// Only the web image holds PUBLIC_ORIGIN. An API-only app can reuse its images.
	if r.o.Deploy.SkipBuild && r.hasWebImage() && r.publicURLWillChange() {
		return fail("use --skip-build", "the public URL changes, and the web image holds PUBLIC_ORIGIN", "run the command again without --skip-build")
	}
	defSize := def.SizeDefault
	if r.env == DefaultEnvName {
		defSize = def.SizeProduction
	}
	r.size = first(r.o.Size, r.st.Size, defSize)
	r.region = first(r.o.Region, r.st.Region, def.Region)
	if r.o.Region == "" {
		r.say("Region: %s (change with --region)", r.region)
	}
	pub, err := server.ReadPublicKey(r.o.SSHKey)
	if err != nil {
		return failWrap("read the operator SSH key", err, "pass --ssh-key <public-key-file>")
	}
	r.pub = pub
	if !r.d.Secrets.Exists(r.root, r.env) && r.o.RecoveryRecipient == "" && !r.o.NoRecovery {
		return fail("create the secrets", "a recovery recipient is required", "pass --recovery-recipient age1... or --no-recovery")
	}
	r.domain = first(r.o.Domain, r.st.Domain)
	return nil
}

func (r *run) loadEnvironmentFile() error {
	envFile, err := deploy.LoadEnvFile(r.root, r.env)
	if errors.Is(err, fs.ErrNotExist) {
		created, scaffoldErr := deploy.ScaffoldEnvFile(r.root, r.env)
		if scaffoldErr != nil {
			return failWrap("create the environment settings", scaffoldErr, "create deploy/environments/"+r.env+".toml from deploy/environments/staging.toml")
		}
		if created {
			r.scaffoldedEnv = true
			r.say("Created deploy/environments/%s.toml from staging", r.env)
			envFile, err = deploy.LoadEnvFile(r.root, r.env)
		}
	}
	if err != nil {
		return failWrap("read the environment settings", err, "create deploy/environments/"+r.env+".toml from deploy/environments/staging.toml")
	}
	r.envFile = envFile
	return nil
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// publicURLWillChange reports whether the run changes the public URL.
func (r *run) publicURLWillChange() bool {
	want := r.o.PublicURL
	if want == "" && r.o.Domain != "" {
		want = "https://" + r.o.Domain
	}
	if want == "" {
		return r.envCfg.PublicURL == ""
	}
	return want != r.envCfg.PublicURL
}

func (r *run) checkTools(ctx context.Context) error {
	for _, tool := range []string{"ssh", "ssh-keygen", "sops", "age-keygen", "git", "docker"} {
		if _, err := r.d.LookPath(tool); err != nil {
			return fail("find the tool "+tool, "it is not on PATH", "install "+tool+", or run spinup doctor")
		}
	}
	if r.o.Deploy.SkipBuild {
		return nil
	}
	if _, err := r.d.Exec.Run(ctx, runexec.Cmd{Name: "docker", Args: []string{"info"}, Dir: r.root}); err != nil {
		return failWrap("reach the Docker daemon", err, "start Docker, or pass --skip-build")
	}
	return nil
}

// ownFiles are the files that provisioning writes and commits. They do not make the tree dirty.
func (r *run) ownFiles() []string {
	files := []string{"spinup.toml", ".sops.yaml", "secrets/", "secrets/" + r.env + ".enc.yaml"}
	if r.scaffoldedEnv {
		files = append(files, "deploy/environments/"+r.env+".toml")
	}
	return files
}

func (r *run) checkClean(ctx context.Context) error {
	res, err := r.d.Exec.Run(ctx, runexec.Cmd{Name: "git", Args: []string{"status", "--porcelain"}, Dir: r.root})
	if err != nil {
		return failWrap("read the git status", err, "run the command inside a git repository")
	}
	own := r.ownFiles()
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if len(line) < 4 {
			continue
		}
		if !slices.Contains(own, strings.TrimSpace(line[3:])) {
			return fail("provision with uncommitted changes", "the git tree is not clean", "commit or stash the changes, or pass --allow-dirty")
		}
	}
	return nil
}

// account reads the account and checks the team (ADR 0017 §5).
func (r *run) account(ctx context.Context) error {
	acct, err := r.d.Cloud.Account(ctx)
	if err != nil {
		return failWrap("read the DigitalOcean account", err, "run spinup deploy digital-ocean login")
	}
	r.team = accountTeam(acct)
	if r.st.Team != "" && r.st.Team != r.team {
		return fail("use this DigitalOcean team", "the environment belongs to team "+r.st.Team+", and the token belongs to team "+r.team, "sign in to the team that owns the environment: spinup deploy digital-ocean login")
	}
	return nil
}

func accountTeam(a digitalocean.Account) string {
	if a.Team != nil && a.Team.UUID != "" {
		return a.Team.UUID
	}
	return a.UUID
}

// accountTeamName returns the display name of the team of the account.
func accountTeamName(a digitalocean.Account) string {
	if a.Team != nil {
		return a.Team.Name
	}
	return ""
}

// checkSize checks the size against the region and the resource limits of the environment.
func (r *run) checkSize(ctx context.Context) error {
	sizes, err := r.d.Cloud.Sizes(ctx)
	if err != nil {
		return failWrap("list the DigitalOcean sizes", err, "run the command again")
	}
	regions, err := r.d.Cloud.Regions(ctx)
	if err != nil {
		return failWrap("list the DigitalOcean regions", err, "run the command again")
	}
	availableRegions := availableRegionSlugs(regions)
	if !slices.Contains(availableRegions, r.region) {
		return fail("use the region "+r.region, "DigitalOcean does not offer an available region with that slug", "pass --region with one of "+strings.Join(availableRegions, ", "))
	}
	need, err := r.minimumCPU()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(sizes, func(s digitalocean.Size) bool { return s.Slug == r.size })
	if i < 0 {
		alternatives, _ := suggestAlternatives(sizes, regions, sizeWant{Slug: r.size, Region: r.region}, need, minMemoryMiB)
		return fail("use the size "+r.size, "DigitalOcean does not offer it", sizeAlternativeTry(alternatives, nil))
	}
	sz := sizes[i]
	if !sz.Available || !slices.Contains(sz.Regions, r.region) {
		alternatives, sizeRegions := suggestAlternatives(sizes, regions, sizeWant{Slug: r.size, Region: r.region}, need, minMemoryMiB)
		return fail("use the size "+r.size, "it is not available in the region "+r.region, sizeAlternativeTry(alternatives, sizeRegions))
	}
	if float64(sz.VCPUs) < need {
		return fail("use the size "+r.size, fmt.Sprintf("it has %d vCPUs, and deploy/environments/%s.toml needs %.1f", sz.VCPUs, r.env, need), "pass a larger --size")
	}
	if sz.Memory < minMemoryMiB {
		return fail("use the size "+r.size, fmt.Sprintf("it has %d MiB of memory, and the stack needs %d MiB", sz.Memory, minMemoryMiB), "pass a larger --size")
	}
	r.sizeInfo = sz
	return nil
}

func (r *run) minimumCPU() (float64, error) {
	services := make([]string, 0, len(r.envFile.Resources))
	for service := range r.envFile.Resources {
		services = append(services, service)
	}
	slices.Sort(services)
	var need float64
	for _, service := range services {
		limits := r.envFile.Resources[service]
		if limits.CPUs == "" {
			continue
		}
		cpu, err := strconv.ParseFloat(limits.CPUs, 64)
		if err != nil {
			return 0, fail("read the cpus limit of "+service, fmt.Sprintf("%q is not a number", limits.CPUs), "correct deploy/environments/"+r.env+".toml")
		}
		need = math.Max(need, cpu)
	}
	return need, nil
}

type sizeWant struct {
	Slug   string
	Region string
}

// suggestAlternatives finds sizes in the region and regions with the requested size.
func suggestAlternatives(sizes []digitalocean.Size, regions []digitalocean.Region, want sizeWant, minCPU float64, minMem int) ([]digitalocean.Size, []string) {
	var alternatives []digitalocean.Size
	for _, size := range sizes {
		if size.Available && slices.Contains(size.Regions, want.Region) && float64(size.VCPUs) >= minCPU && size.Memory >= minMem {
			alternatives = append(alternatives, size)
		}
	}
	slices.SortFunc(alternatives, func(a, b digitalocean.Size) int {
		if a.PriceMonthly < b.PriceMonthly {
			return -1
		}
		if a.PriceMonthly > b.PriceMonthly {
			return 1
		}
		return strings.Compare(a.Slug, b.Slug)
	})
	if len(alternatives) > 3 {
		alternatives = alternatives[:3]
	}
	var available []string
	for _, region := range regions {
		if region.Available && region.Slug != want.Region && slices.Contains(region.Sizes, want.Slug) {
			available = append(available, region.Slug)
		}
	}
	slices.Sort(available)
	if len(available) > 3 {
		available = available[:3]
	}
	return alternatives, available
}

func availableRegionSlugs(regions []digitalocean.Region) []string {
	slugs := make([]string, 0, len(regions))
	for _, region := range regions {
		if region.Available {
			slugs = append(slugs, region.Slug)
		}
	}
	slices.Sort(slugs)
	return slugs
}

func sizeAlternativeTry(sizes []digitalocean.Size, regions []string) string {
	parts := make([]string, 0, 2)
	if len(sizes) > 0 {
		choices := make([]string, len(sizes))
		for i, size := range sizes {
			choices[i] = fmt.Sprintf("%s ($%.2f/month)", size.Slug, size.PriceMonthly)
		}
		parts = append(parts, "pass --size "+strings.Join(choices, ", "))
	}
	if len(regions) > 0 {
		parts = append(parts, "pass --region "+strings.Join(regions, ", "))
	}
	if len(parts) == 0 {
		return "pass another --size or --region"
	}
	return strings.Join(parts, ", or ")
}

func money(monthly, hourly float64) string {
	return fmt.Sprintf("$%.2f/month ($%.5f/hour)", monthly, hourly)
}

// hasWebImage reports whether the app builds a web image (deploy/Dockerfile.web).
func (r *run) hasWebImage() bool {
	_, err := os.Stat(filepath.Join(r.root, "deploy", "Dockerfile.web"))
	return err == nil
}
