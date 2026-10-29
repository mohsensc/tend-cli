package verify

import (
	"slices"
	"testing"
)

func TestFilter(t *testing.T) {
	with := func(gofl string, kv ...string) []string {
		res := slices.Clone(kv)
		res = append(res, "CI=1", "GOFLAGS="+gofl, "HUSKY=0", "TEND=1", "npm_config_audit=false", "npm_config_fund=false")
		return res
	}
	tests := []struct {
		name string
		env  []string
		keep []string
		want []string
	}{
		{"empty", nil, nil, with("-mod=mod")},
		{
			"plain vars pass",
			[]string{"HOME=/home/x", "PATH=/bin", "LANG=C.UTF-8", "TMPDIR=/tmp", "NODE_ENV=test"},
			nil,
			with("-mod=mod", "HOME=/home/x", "PATH=/bin", "LANG=C.UTF-8", "TMPDIR=/tmp", "NODE_ENV=test"),
		},
		{
			"secrets by name",
			[]string{"GITHUB_TOKEN=a", "GH_TOKEN=b", "MODEL_API_KEY=c", "PAYMENTS_API_KEY=d", "AWS_SECRET_ACCESS_KEY=e",
				"DB_PASSWORD=f", "SMTP_PASSWD=g", "STRIPE_APIKEY=h", "x_api-key=i", "SSH_PRIVATE=j", "GOOGLE_CREDENTIALS=k",
				"npm_config_//registry.npmjs.org/:_authToken=l", "USER=me"},
			nil,
			with("-mod=mod", "USER=me"),
		},
		{
			"secrets by shape",
			[]string{"STRIPE_KEY=a", "SIGNING_KEY=b", "GH_PAT=c", "VAULT_ROOT_TOKEN=d", "APP_SECRET=e", "MYPASSWORDFILE=f",
				"GCP_CREDENTIALS=g", "db_password=h", "KEYBOARD=us", "PATH=/bin", "PATTERN=x", "KEY=k"},
			nil,
			with("-mod=mod", "KEYBOARD=us", "PATH=/bin", "PATTERN=x", "KEY=k"),
		},
		{
			"keep lets a shaped name through",
			[]string{"STRIPE_KEY=a", "GH_PAT=b", "TEST_DB_PASSWORD=c"},
			[]string{"STRIPE_KEY", "TEST_DB_PASSWORD"},
			with("-mod=mod", "STRIPE_KEY=a", "TEST_DB_PASSWORD=c"),
		},
		{
			"tend vars dropped",
			[]string{"TEND_HOME=/x", "TEND_GIT_USER=bot", "TENDER=keep"},
			nil,
			with("-mod=mod", "TENDER=keep"),
		},
		{
			"keep adds back",
			[]string{"NPM_TOKEN=a", "DB_PASSWORD=b", "TEND_LOG=debug"},
			[]string{"NPM_TOKEN", "TEND_LOG", "ABSENT"},
			with("-mod=mod", "NPM_TOKEN=a", "TEND_LOG=debug"),
		},
		{
			"ours override",
			[]string{"CI=true", "TEND=0", "npm_config_fund=true"},
			nil,
			with("-mod=mod"),
		},
		{"goflags kept and extended", []string{"GOFLAGS=-count=1"}, nil, with("-count=1 -mod=mod")},
		{"goflags with mod untouched", []string{"GOFLAGS=-mod=vendor"}, nil, with("-mod=vendor")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Filter(tt.env, tt.keep); !slices.Equal(got, tt.want) {
				t.Errorf("Filter =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}
