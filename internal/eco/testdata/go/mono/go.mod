module example.com/app

go 1.24

require (
	github.com/BurntSushi/toml v1.4.0
	github.com/google/uuid v1.6.0
	golang.org/x/mod v0.21.0 // indirect
	example.com/forked v1.2.0
	example.com/pinned v1.0.0
	github.com/davecgh/go-spew v1.1.1 // indirect
)

require gopkg.in/yaml.v3 v3.0.1

replace example.com/forked => ../forked

replace example.com/pinned v1.0.0 => example.com/pinned-fork v1.0.1
