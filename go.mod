module github.com/m1cha/lxdocker

go 1.18

require (
	github.com/google/go-containerregistry v0.11.0
	github.com/lxc/lxd v0.0.0-20220819011414-f94291c528fa
	github.com/opencontainers/image-spec v1.0.3-0.20220114050600-8b9d41f48198
	go.uber.org/zap v1.22.0
	golang.org/x/term v0.0.0-20220722155259-a9ba230a4035
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/containerd/stargz-snapshotter/estargz v0.12.0 // indirect
	github.com/docker/cli v20.10.17+incompatible // indirect
	github.com/docker/distribution v2.8.1+incompatible // indirect
	github.com/docker/docker v20.10.17+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.6.4 // indirect
	github.com/flosch/pongo2 v0.0.0-20200913210552-0d938eb266f3 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/inconshreveable/mousetrap v1.0.1 // indirect
	github.com/kballard/go-shellquote v0.0.0-20180428030007-95032a82bc51 // indirect
	github.com/klauspost/compress v1.15.9 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/pborman/uuid v1.2.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/sirupsen/logrus v1.9.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/vbatts/tar-split v0.11.2 // indirect
	go.uber.org/atomic v1.10.0 // indirect
	go.uber.org/multierr v1.8.0 // indirect
	golang.org/x/crypto v0.0.0-20220817201139-bc19a97f63c8 // indirect
	golang.org/x/sync v0.0.0-20220819030929-7fc1605a5dde // indirect
	golang.org/x/sys v0.0.0-20220818161305-2296e01440c6 // indirect
)

require (
	github.com/spf13/cobra v1.5.0
	pkg/common v1.0.0
)

replace pkg/common => ./pkg/common
