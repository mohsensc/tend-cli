package eco

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/ver"
)

const tfMain = `terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 4.0"
    }
    random = {
      version = ">= 3.1"
    }
  }
}

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "5.0.0"
  name    = "main"
}

module "local" {
  source = "./modules/net"
}
`

const tfLock = `# This file is maintained automatically by "terraform init".
provider "registry.terraform.io/hashicorp/aws" {
  version     = "4.67.0"
  constraints = "~> 4.0"
  hashes = [
    "h1:abc=",
  ]
}
`

func TestTerraformDeps(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"infra/main.tf": tfMain, "infra/.terraform.lock.hcl": tfLock, "infra/variables.tf": "variable \"x\" {}\n"})
	ms := manifestsIn(t, terraform{}, root)
	if len(ms) != 1 || ms[0].Lock != "infra/.terraform.lock.hcl" {
		t.Fatalf("manifests = %+v", ms)
	}
	ds, err := terraform{}.Deps(root, ms[0])
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range ds {
		got = append(got, fmt.Sprintf("%d %s %s (%s)", d.Line, d.Name, d.Version, d.Req))
	}
	want := []string{"6 hashicorp/aws 4.67.0 (~> 4.0)", "9 hashicorp/random 3.1 (>= 3.1)", "16 terraform-aws-modules/vpc/aws 5.0.0 (5.0.0)"}
	if !slices.Equal(got, want) {
		t.Errorf("deps = %q, want %q", got, want)
	}
}

// TestTerraformConstraint covers how Terraform writes constraints: each comes
// out admitting the target.
func TestTerraformConstraint(t *testing.T) {
	tests := []struct{ c, to, want string }{
		{"~> 4.0", "5.1.0", "~> 5.1"},
		{"~> 4.0", "4.67.0", "~> 4.0"},
		{"~> 4.1.2", "4.1.9", "~> 4.1.2"},
		{"~> 4.1.2", "4.2.0", "~> 4.2.0"},
		{">= 3.1", "3.6.0", ">= 3.6.0"},
		{"= 4.67.0", "5.1.0", "= 5.1.0"},
		{"5.0.0", "5.8.1", "5.8.1"},
		{">= 4.0, < 5.0", "5.1.0", ">= 4.0, < 6.0"},
		{">= 4.0, < 5.0", "4.2.0", ">= 4.0, < 5.0"},
		{"~> 4.0, != 4.3.0", "5.0.0", "~> 5.0, != 4.3.0"},
		{"<= 4.2", "5.0.0", "<= 5.0.0"},
	}
	for _, tt := range tests {
		got, err := fixReq(tt.c, tt.to, false)
		if err != nil || got != tt.want {
			t.Errorf("fixReq(%q, %s) = %q, %v; want %q", tt.c, tt.to, got, err, tt.want)
			continue
		}
		if v, _ := ver.Parse(tt.to); !admits(got, v, false) {
			t.Errorf("%q does not admit %s", got, tt.to)
		}
	}
}

const tfLockTwo = `# This file is maintained automatically by "terraform init".
# Manual edits may be lost in future updates.

provider "registry.terraform.io/hashicorp/aws" {
  version     = "4.67.0"
  constraints = "~> 4.0"
  hashes = [
    "h1:abc=",
  ]
}

provider "registry.terraform.io/hashicorp/random" {
  version     = "3.1.0"
  constraints = ">= 3.1"
  hashes = [
    "h1:old=",
  ]
}
`

// TestTerraformBumpLock runs the bump against a terraform that upgrades
// every provider it may: only the bumped one keeps its move, and it lands on
// the target even where the constraint allows newer.
func TestTerraformBumpLock(t *testing.T) {
	tests := []struct {
		name   string
		moved  string // what terraform init -upgrade leaves in the lock
		want   string // the lock after the bump; "" for the error
		errHas string
	}{
		{
			name: "others restored",
			moved: strings.NewReplacer(`"4.67.0"`, `"5.1.0"`, `"~> 4.0"`, `"= 5.1.0"`, `"h1:abc="`, `"h1:new="`,
				`"3.1.0"`, `"3.7.2"`, `"h1:old="`, `"h1:newer="`).Replace(tfLockTwo),
			want: strings.NewReplacer(`"4.67.0"`, `"5.1.0"`, `"~> 4.0"`, `"= 5.1.0"`, `"h1:abc="`, `"h1:new="`).Replace(tfLockTwo),
		},
		{
			name:   "past the target",
			moved:  strings.NewReplacer(`"4.67.0"`, `"5.2.0"`).Replace(tfLockTwo),
			errHas: "hashicorp/aws locked at 5.2.0, not 5.1.0",
		},
		{
			name:   "dropped",
			moved:  tfLockTwo[strings.Index(tfLockTwo, `provider "registry.terraform.io/hashicorp/random"`):],
			errHas: "hashicorp/aws is not in the lock",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, map[string]string{"main.tf": tfMain, ".terraform.lock.hcl": tfLockTwo})
			moved := filepath.Join(t.TempDir(), "moved")
			seen := filepath.Join(t.TempDir(), "seen")
			if err := os.WriteFile(moved, []byte(tt.moved), 0o644); err != nil {
				t.Fatal(err)
			}
			logged := toolScript(t, "terraform", `case "$*" in *-upgrade*) cat main.tf > `+seen+`; cp `+moved+` .terraform.lock.hcl;; esac`+"\n")
			d := depsIn(t, terraform{}, root, "main.tf")[0]
			_, err := terraform{}.Bump(context.Background(), root, d, "5.1.0", "")
			if tt.want == "" {
				if err == nil || !strings.Contains(err.Error(), tt.errHas) {
					t.Fatalf("err = %v, want %q", err, tt.errHas)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			snap := treeSnapshot(t, root)
			if snap[".terraform.lock.hcl"] != tt.want {
				t.Errorf("lock =\n%s\nwant\n%s", snap[".terraform.lock.hcl"], tt.want)
			}
			if !strings.Contains(snap["main.tf"], `version = "~> 5.1"`) {
				t.Errorf("main.tf =\n%s", snap["main.tf"])
			}
			pass, _ := os.ReadFile(seen)
			if !strings.Contains(string(pass), `version = "= 5.1.0"`) {
				t.Errorf("the upgrade pass saw\n%s", pass)
			}
			want := []string{". init -backend=false -input=false -upgrade", ". init -backend=false -input=false"}
			if got := logged(root); !slices.Equal(got, want) {
				t.Errorf("commands = %q, want %q", got, want)
			}
		})
	}
}

func TestTerraformMetaAndBump(t *testing.T) {
	f := replayCanned(t,
		canned{url: "https://registry.terraform.io/v1/providers/hashicorp/aws/versions", body: `{"versions": [{"version": "5.1.0"}, {"version": "4.67.0"}, {"version": "6.0.0-beta1"}]}`},
		canned{url: "https://registry.terraform.io/v1/modules/terraform-aws-modules/vpc/aws/versions", body: `{"modules": [{"versions": [{"version": "5.8.1"}, {"version": "5.0.0"}]}]}`},
		canned{url: "https://registry.terraform.io/v1/providers/hashicorp/nope/versions", status: 404, body: `{"errors":["Not Found"]}`},
	)
	m, err := terraform{}.Meta(context.Background(), f, "hashicorp/aws")
	if err != nil || len(m.Releases) != 3 || m.Releases[2].Version != "6.0.0-beta1" || !m.Releases[2].Pre {
		t.Fatalf("provider meta = %+v, %v", m, err)
	}
	if m, err := (terraform{}).Meta(context.Background(), f, "terraform-aws-modules/vpc/aws"); err != nil || m.Releases[1].Version != "5.8.1" {
		t.Fatalf("module meta = %+v, %v", m, err)
	}
	if _, err := (terraform{}).Meta(context.Background(), f, "hashicorp/nope"); err == nil {
		t.Error("a missing provider has metadata")
	}

	hideTools(t)
	root := t.TempDir()
	write(t, root, map[string]string{"main.tf": tfMain, ".terraform.lock.hcl": tfLock})
	ds := depsIn(t, terraform{}, root, "main.tf")
	b, err := terraform{}.Bump(context.Background(), root, ds[0], "5.1.0", "")
	if err != nil || b.Locked || b.Note != "terraform not found in PATH" || !slices.Equal(b.Files, []string{"main.tf"}) {
		t.Fatalf("bump = %+v, %v", b, err)
	}
	if _, err := (terraform{}).Bump(context.Background(), root, ds[2], "5.8.1", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "main.tf"))
	want := strings.Replace(tfMain, `version = "~> 4.0"`, `version = "~> 5.1"`, 1)
	want = strings.Replace(want, `version = "5.0.0"`, `version = "5.8.1"`, 1)
	if string(got) != want {
		t.Errorf("main.tf =\n%s", got)
	}
	if c := (terraform{}).Commands(root, ds[0].Manifest); len(c.Test) != 0 {
		t.Errorf("commands without terraform installed: %+v", c)
	}
}
