package resource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stopyellingatme/spinup/internal/gen"
	livegen "github.com/stopyellingatme/spinup/internal/gen/live"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func init() {
	registry.Register(gen.Generator{Name: "live", Short: "Add a live feed to an existing resource", Usage: "<resource>", Example: "  # Add a live feed for notes\n  spinup g live note", Run: runLive})
}

// ExistingSpec reads the table and handler guard without requiring audit.
// Fields is empty. The resource must have a handler file.
func ExistingSpec(root, target string) (Spec, error) {
	name, def, found, err := existingTable(root, target)
	if err != nil {
		return Spec{}, err
	}
	missing := fmt.Errorf("cannot generate a live feed for %s because no resource handler exists\nTry: %s", target, "spinup g resource "+target+" <field:type>...")
	if !found {
		return Spec{}, missing
	}
	source, err := os.ReadFile(filepath.Join(root, handlersPath(name)))
	if errors.Is(err, os.ErrNotExist) {
		return Spec{}, missing
	}
	if err != nil {
		return Spec{}, fmt.Errorf("cannot read resource handler: %w\nTry: check the file permissions", err)
	}
	owned, valid := tableOwner(def)
	if !valid {
		return Spec{}, fmt.Errorf("cannot generate a live feed for %s because user_id is not an integer owner column\nTry: use an integer owner column", target)
	}
	spec := Spec{Root: root, Name: name, Owned: owned, Shared: !owned}
	cfg, err := loadResourceConfig(root)
	if err != nil {
		return Spec{}, err
	}
	spec.Roles, err = getHistoryRoles(source, spec, cfg)
	if err != nil {
		return Spec{}, err
	}
	return spec, nil
}

func addLive(spec Spec, in gen.Input, out *gen.Output) (*gen.Output, error) {
	if err := livegen.RequirePack(in.Lock); err != nil {
		return nil, err
	}
	path := livegen.FeedPath(spec.Name)
	if _, err := os.Stat(filepath.Join(spec.Root, path)); err == nil && !Bool(in.Flags, "force") {
		return nil, fmt.Errorf("cannot generate a live feed because %s already exists\nTry: pass --force to replace the file", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("cannot inspect %s: %w\nTry: check the file permissions", path, err)
	}
	module, err := apiModule(spec.Root)
	if err != nil {
		return nil, err
	}
	data, err := livegen.FeedSource(livegen.FeedSpec{Module: module, Name: spec.Name, Owned: spec.Owned, Shared: spec.Shared, Roles: spec.Roles})
	if err != nil {
		return nil, err
	}
	file := vfs.File{Path: path, Data: data, Class: string(manifest.Scaffold)}
	generated, err := livegen.RenderGenerated(spec.Root, []vfs.File{file})
	if err != nil {
		return nil, err
	}
	out.Plan, err = out.Plan.Merge(vfs.Plan{Files: []vfs.File{file, generated}})
	if err != nil {
		return nil, fmt.Errorf("cannot plan live feeds: %w\nTry: report this generator bug", err)
	}
	out.Edits = append(out.Edits, livegen.GeneratedPath)
	return out, nil
}

func runLive(_ context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) != 1 {
		return nil, fmt.Errorf("cannot generate a live feed because one resource name is required\nTry: spinup g live <resource>")
	}
	if err := livegen.RequirePack(in.Lock); err != nil {
		return nil, err
	}
	spec, err := ExistingSpec(in.Root, in.Args[0])
	if err != nil {
		return nil, err
	}
	// Inspect the existing handler to choose the transaction wrapper it uses.
	handler, err := os.ReadFile(filepath.Join(in.Root, handlersPath(spec.Name)))
	if err != nil {
		return nil, fmt.Errorf("cannot read resource handler: %w\nTry: check the file permissions", err)
	}
	spec.Audit.On = strings.Contains(string(handler), "audit.InTx(")
	next := []string{"if the live module is new: spinup db migrate up"}
	for _, op := range []string{"Create", "Update", "Delete"} {
		change := "live.Upsert(strconv.FormatInt(row.ID, 10), item)"
		if op == "Delete" {
			change = "live.Delete(strconv.FormatInt(req.Id, 10))"
		}
		publish := "return live.Publish(ctx, tx, " + liveKey(spec) + ", " + change + ")"
		if op == "Delete" {
			publish = "if deleted > 0 { " + publish + " }; return nil"
		}
		// The comments identify where the app's existing query and conversion belong.
		code := "// " + op + spec.Name.Pascal() + "\nerr = " + liveTx(spec) + "(ctx, s.DB, func(tx pgx.Tx) error {\n\t// Run the " + strings.ToLower(op) + " query with s.Queries.WithTx(tx). Return any query error.\n"
		if op != "Delete" {
			code += "\t// Convert row to item here. Return any conversion error.\n"
		}
		next = append(next, code+"\t"+publish+"\n})")
	}
	page := filepath.Join(spec.Root, pagesDir(spec.Name), "+page.svelte")
	if _, err := os.Stat(page); err == nil {
		next = append(next, "import { liveRefresh } from '#lib/live.svelte.js';\nliveRefresh('"+spec.Name.PluralSnake()+"');")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("cannot inspect list page: %w\nTry: check the file permissions", err)
	}
	return addLive(spec, in, &gen.Output{Next: next, PostSteps: []gen.PostStep{gen.PostGofmt}})
}

func liveKey(spec Spec) string {
	if spec.Owned {
		return fmt.Sprintf("live.OwnedKey(%q, user.ID)", spec.Name.PluralSnake())
	}
	return fmt.Sprintf("live.SharedKey(%q)", spec.Name.PluralSnake())
}

func liveTx(spec Spec) string {
	if spec.Audit.On {
		return "audit.InTx"
	}
	return "db.InTx"
}

// liveUpsert converts the row before commit and publishes the returned item.
func (rc resourceCode) liveUpsert(g *goFile, withErr bool) {
	g.p("if err != nil {return err}")
	if withErr {
		g.p("item, err = %sFromStore(row)", rc.conv)
		g.p("if err != nil {return err}")
	} else {
		g.p("item = %sFromStore(row)", rc.conv)
	}
	g.p("return live.Publish(ctx, tx, %s, live.Upsert(strconv.FormatInt(row.ID, 10), item))", liveKey(rc.spec))
}
