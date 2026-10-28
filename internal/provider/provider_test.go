package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/itlabsio/terraform-provider-gemini/internal/client"
)

// fakeAPI — in-memory подмножество /api/* Gemini с той же семантикой, что у
// backend: строгий JSON (DisallowUnknownFields), пустой пароль в PUT — «не
// менять», проект с инстансами не удаляется.
type fakeAPI struct {
	mu        sync.Mutex
	projects  map[string]client.Project
	instances map[string]client.Instance
	passwords map[string]string
	seq       int
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		projects:  map[string]client.Project{},
		instances: map[string]client.Instance{},
		passwords: map[string]string{},
	}
}

func (f *fakeAPI) password(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, inst := range f.instances {
		if inst.Name == name {
			return f.passwords[id]
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-token" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no token"})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch {
	case p == "/api/projects" && r.Method == http.MethodGet:
		out := []client.Project{}
		for _, pr := range f.projects {
			pr.InstanceCount = 0
			for _, i := range f.instances {
				if i.Project == pr.Name {
					pr.InstanceCount++
				}
			}
			out = append(out, pr)
		}
		writeJSON(w, http.StatusOK, out)
	case p == "/api/projects" && r.Method == http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		if !decode(w, r, &req) {
			return
		}
		if _, ok := f.projects[req.Name]; ok {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "conflict"})
			return
		}
		pr := client.Project{Name: req.Name, CreatedAt: time.Now()}
		f.projects[req.Name] = pr
		writeJSON(w, http.StatusCreated, pr)
	case strings.HasPrefix(p, "/api/projects/") && r.Method == http.MethodDelete:
		name := strings.TrimPrefix(p, "/api/projects/")
		if _, ok := f.projects[name]; !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		for _, i := range f.instances {
			if i.Project == name {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "conflict"})
				return
			}
		}
		delete(f.projects, name)
		w.WriteHeader(http.StatusNoContent)
	case p == "/api/instances" && r.Method == http.MethodGet:
		out := []client.Instance{}
		for _, i := range f.instances {
			out = append(out, i)
		}
		writeJSON(w, http.StatusOK, out)
	case p == "/api/instances" && r.Method == http.MethodPost:
		var in client.InstanceInput
		if !decode(w, r, &in) || !f.checkProject(w, in) {
			return
		}
		for _, i := range f.instances {
			if i.Name == in.Name {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "conflict"})
				return
			}
		}
		f.seq++
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", f.seq)
		inst := apply(client.Instance{ID: id, CreatedAt: time.Now()}, in)
		f.instances[id] = inst
		f.passwords[id] = in.PlainPassword
		writeJSON(w, http.StatusCreated, inst)
	case strings.HasPrefix(p, "/api/instances/"):
		id := strings.TrimPrefix(p, "/api/instances/")
		inst, ok := f.instances[id]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, inst)
		case http.MethodPut:
			var in client.InstanceInput
			if !decode(w, r, &in) || !f.checkProject(w, in) {
				return
			}
			f.instances[id] = apply(inst, in)
			if in.PlainPassword != "" {
				f.passwords[id] = in.PlainPassword
			}
			writeJSON(w, http.StatusOK, f.instances[id])
		case http.MethodDelete:
			delete(f.instances, id)
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no route"})
	}
}

func (f *fakeAPI) checkProject(w http.ResponseWriter, in client.InstanceInput) bool {
	if in.Project != nil && *in.Project != "" {
		if _, ok := f.projects[*in.Project]; !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown project " + *in.Project})
			return false
		}
	}
	return true
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return false
	}
	return true
}

func apply(inst client.Instance, in client.InstanceInput) client.Instance {
	inst.Role = in.Role
	if in.Project != nil {
		inst.Project = *in.Project
	}
	inst.Name, inst.Host, inst.Port = in.Name, in.Host, in.Port
	inst.SSLMode, inst.SSLRootCert, inst.DiscoveryDB = in.SSLMode, in.SSLRootCert, in.DiscoveryDB
	inst.ExcludedDatabases = in.ExcludedDatabases
	inst.AuthType, inst.PlainUsername = in.AuthType, in.PlainUsername
	inst.VaultPath, inst.VaultRole = in.VaultPath, in.VaultRole
	inst.VaultUsernameKey, inst.VaultPasswordKey = in.VaultUsernameKey, in.VaultPasswordKey
	return inst
}

func factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"gemini": providerserver.NewProtocol6WithError(New("test")()),
	}
}

func providerBlock(url string) string {
	return fmt.Sprintf(`
provider "gemini" {
  url   = %q
  token = "test-token"
}
`, url)
}

func instanceConfig(url, name, host, password string, version int) string {
	return providerBlock(url) + fmt.Sprintf(`
resource "gemini_project" "p" {
  name = "billing"
}

resource "gemini_source_instance" "i" {
  name                      = %q
  project                   = gemini_project.p.name
  host                      = %q
  ssl_mode                  = "verify-full"
  excluded_databases        = ["tmp", "scratch"]
  auth_type                 = "plain"
  plain_username            = "backup"
  plain_password_wo         = %q
  plain_password_wo_version = %d
}
`, name, host, password, version)
}

func TestSourceInstanceLifecycle(t *testing.T) {
	api := newFakeAPI()
	srv := httptest.NewServer(api)
	defer srv.Close()

	var firstID string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: instanceConfig(srv.URL, "core", "db1", "p1", 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("gemini_project.p", "id", "billing"),
					resource.TestCheckResourceAttr("gemini_source_instance.i", "project", "billing"),
					resource.TestCheckResourceAttr("gemini_source_instance.i", "port", "5432"),
					resource.TestCheckResourceAttr("gemini_source_instance.i", "discovery_db", ""),
					resource.TestCheckResourceAttr("gemini_source_instance.i", "excluded_databases.#", "2"),
					resource.TestCheckNoResourceAttr("gemini_source_instance.i", "plain_password_wo"),
					func(s *terraform.State) error {
						firstID = s.RootModule().Resources["gemini_source_instance.i"].Primary.ID
						if got := api.password("core"); got != "p1" {
							return fmt.Errorf("password on create = %q, want p1", got)
						}
						return nil
					},
				),
			},
			{
				// Новый пароль без смены версии не отправляется.
				Config: instanceConfig(srv.URL, "core", "db2", "p2", 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("gemini_source_instance.i", plancheck.ResourceActionUpdate),
				}},
				Check: func(*terraform.State) error {
					if got := api.password("core"); got != "p1" {
						return fmt.Errorf("password without version bump = %q, want p1", got)
					}
					return nil
				},
			},
			{
				Config: instanceConfig(srv.URL, "core", "db2", "p2", 2),
				Check: func(*terraform.State) error {
					if got := api.password("core"); got != "p2" {
						return fmt.Errorf("password after version bump = %q, want p2", got)
					}
					return nil
				},
			},
			{
				ResourceName:            "gemini_source_instance.i",
				ImportState:             true,
				ImportStateId:           "core",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"plain_password_wo_version"},
			},
			{
				// Переименования нет — пересоздание.
				Config: instanceConfig(srv.URL, "core2", "db2", "p2", 2),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("gemini_source_instance.i", plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: func(s *terraform.State) error {
					if s.RootModule().Resources["gemini_source_instance.i"].Primary.ID == firstID {
						return fmt.Errorf("instance was not recreated")
					}
					return nil
				},
			},
		},
	})
}

func TestSourceInstanceValidation(t *testing.T) {
	cases := map[string]struct {
		body string
		err  string
	}{
		"root cert without verify": {`
  ssl_mode       = "require"
  ssl_root_cert  = "-----BEGIN CERTIFICATE-----"
  auth_type      = "plain"
  plain_username = "u"`, `ssl_root_cert requires a verifying ssl_mode`},
		"plain without username": {`
  auth_type = "plain"`, `plain_username is required`},
		"vault without path": {`
  auth_type = "vault"`, `vault_path is required`},
		"half vault keys": {`
  auth_type          = "vault"
  vault_path         = "secret/db"
  vault_username_key = "user"`, `vault keys must be set together`},
		"bad name": {`
  auth_type      = "plain"
  plain_username = "u"`, `starting and ending with a letter`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			instName := "ok"
			if name == "bad name" {
				instName = "-bad"
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps: []resource.TestStep{{
					Config: providerBlock("http://127.0.0.1:1") + fmt.Sprintf(`
resource "gemini_source_instance" "i" {
  name = %q
  host = "h"%s
}
`, instName, c.body),
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(c.err),
				}},
			})
		})
	}
}
