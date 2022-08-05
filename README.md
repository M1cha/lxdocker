# lxdocker

This project provides both a CLI tool and an image server for using docker
images with [LXD](https://linuxcontainers.org/lxd/).

## Why?
LXD was made for system-containers, not application-containers, yet:
- running docker or podman side-by side on the same host may not be the best
  idea due to both security and interoperability problems
- running docker or podman inside LXD is possible but comes with it's own
  complications. I've noticed horrible performance when LXD uses ZFS. I also
  had kernel errors when using a BTRFS loop image inside a ZFS filesystem.
  I had to use a separate BTRFS partition to make that work. You could use
  their native ZFS support but ZFS nesting is still an
  [experimental feature](https://github.com/lxc/lxd/issues/4184).
- LXDs instance configuration options are superior. Just look at the networking
  section, or it's support for forwarding all kinds of devices like disks, USB,
  GPUs and much more. With docker you only have the option to forward device
  nodes using `--device` and in some cases even have to revert to
 `--priviledged`.
- LXD is WAY more secure than Docker. Docker needs a lot of work to get it
  anywhere even close to being acceptable. And even if you managed to protect
  the host from all of your containers, you still have to protect the
  containers from each other. User namespaces are not enough since you also
  need a proper network setup at which point you might run into limitations
  mentioned in the previous point.
- Docker is ineffecient by default. port-farwardings use a proxy server and the
  docker daemon can use a lot of resources that might not be available to e.g.
  single-board-computers.
- `podman` is pretty good and also supports apparmor, seccomp and user
  namespaces when run in rootful mode. The only issue I have with it is that
  due it it being modular, it really only provides a simple container runtime
  without any of the goodies you get with docker. At that point you might ask
  yourself why you're using podman instead of lxdocker given all the other
  points in this list 😉
- When already running LXD anyway, lxdocker makes it easier to connect
  traditional LXD instances with lxdocker instances. For example, LXD runs a
  DNS server on lxdbr0 so you can resolve all instance IPs by their names.
- I run lots of non-critical containers which I want to update automatically.
  Converting docker-images to LXD-images lets me use LXDs
  [Auto-update](https://linuxcontainers.org/lxd/docs/master/image-handling/#auto-update)
  feature so all that's left to do is write a daemon that listens for LXD image
  updates and recreates containers. This way I only need one solution that
  works for both LXD and docker containers. As soon as I have finished writing
  that daemon I'll put a link here.

# Why use lxdocker instead of native LXD images?
- some software like [PhotoPrism](https://docs.photoprism.app/getting-started/#setup) is only available as docker images
- It uses less resources. Even Alpine runs many services via openrc.

# How does it work?

Here's some facts:
- you can't download a full index of a public docker registry
- `imgserver` can't pull, convert and serve images on the fly because the
  simplestreams protocol needs to know the checksum of the image beforehand
- some images might need changes to get them working
- you might want to add usecase specific changes to some images because
  [cloud-init](https://cloudinit.readthedocs.io) doesn't work with them.

So the idea is that you provide a directory with one yaml file per LXD image
that you want imgserver to serve. The filename (without the extension) will be
used as the image name. The contents specify the image source and additional
changes that should be applied That's similar to what
[distrobuilder](https://distrobuilder.readthedocs.io/) does.

The recommended setup to run this inside a LXD container which updates the
generated images with a cron-job and to enable LXDs auto-update so you always
have the latest images in your LXD image cache and don't have to worry about
lxdocker anymore.
For simple setups, the LXD host, the device running `lxdocker`, and the device
running the `lxc` CLI are all the the system. This is also what the rest of the
README assumes.
[Here](https://github.com/M1cha/homeserver/blob/main/configs/lxd/instances/lxdocker.yaml) is the instance config I use for the lxdocker container.

## YAML image specification format
Currently this is quite simple but may be extended as the need rises.

Sample with all available options:
```yaml
---
image: library/nginx:lates
disable_supervisor: false
```

### `image` (required)
The source to pull the image from. Byt default this uses the docker hub
registry but it can also contain a URL like `ghcr.io/home-assistant/home-assistant:stable`.

### `disable_supervisor` (optional, default: false)
By default, the generated images run `busybox sh` ad PID 1 and use it as a
simple supervisor to translate shutdown signals and (in future) support
auto-restart.
Some containers (like home-assistant) may already provide that e.g. through the
S6 supervisor. Not only does it provide the same functionality, but it also
expects to run as PID 1 so it won't run without this option set to `true`.

## Unconfigurable changes applied to images
- `/busybox-lxd`: A statically linked busybox is put here so a custom init
   script can perform required initialization
- `/lxd-udhcpc-default.script`: A wrapper for switching to LXDs busybox since
  busybox might not be installed in the container image.
- `/lxd-udhcpc-default.script.real`: a copy from `/etc/udhcpc/default.script`
- renames `/sbin/init` to `/lxd-realinit` if it exists so we can run or own
  code before the container starts.
- writes a script to `/sbin/init`, see below for more details

### /sbin/init
containers usually require the runtime to do certain initialization before they
are run. What the script currently does:
- set environment variables specified in the OCI image. Existing variables
  won't be overridden so you can use LXDs image configuration to change their
  values
- set working directory as specified in the OCI image
- mount shmfs to `/dev/shm`: That's required by a few containers
- disable IPv4 and IPv6 forwarding. Containers usually aren't used as routers
  and this prevents potential security issues by default. This is especially
  useful if you're exposing a container on your local network via a `macvlan`
  since containers usually don't setup a firewall.
- start `udhcpc` to configure `eth0`: Containers expect it to be setup already
- bind-mount `/lxd-realinit` to `/sbin/init`: To prevent compatibility issues
  due to lxdocker having replaced that binary.
- run entrypoint with optional arguments as specified in the OCI image
- if `disable_supervisor: false`, supervises the entrypoint process

## Image metadata
LXD images contain a metadata.yaml with additional information. Here's what
that looks like:
```yaml
architecture: amd64
creation_date: 1659595589
expiry_date: 0
properties:
    description: nginx
templates:
    /etc/hostname:
        when:
            - create
            - copy
        create_only: false
        template: hostname.tpl
        properties: {}
    /etc/hosts:
        when:
            - create
            - copy
        create_only: false
        template: hosts.tpl
        properties: {}
```

## `lxdocker`
This CLI tool pulls docker images from a registry and converts them to LXD
images. It acts on a directory rather than a single file so it has a complete
list of images and can delete generated files that are not part of any yaml
image specification anymore.

### Requirements
- a statically linked busybox in `/bin/busybox` Debian package: `busybox-static`
- `/etc/udhcpc/default.script`. Debian package: `udhcpc`
- `sqfstar` if `squashfs` is used. Debian package: `squashfs-tools` (only on `unstable`)

### CLI options

#### `--cache PATH` (required)
Path to lxdockers cache directory. This is where OCI layers are being
downloaded to. Unused data is automatically removed after every run.

#### `--lxdimages PATH` (required)
This is where lxdocker stores the generated images. Old and unused versions
are automatically removed after every run.

#### `--specs PATH` (required)
This directory should contain your yaml specifications for how to generate
LXD images.

#### `--imageformat FORMAT` (optional)
The format of the generated rootfs. Supported values:

- `squashfs`: default, because it supports parallell (de-)compression. Requires
  `sqfstar` which is only available in newer versions of `squashfs-tools`.
- `gzip`: Alternative which neither lxdocker nor LXD (currently) support
  parallel (de-)compression for.
- `tar`: uncompressed. Might be a good fit if you have very fast disks and
  networking and don't worry about disk usage.

## `imgserver`
This is a [simplestreams image server](https://linuxcontainers.org/lxd/docs/master/image-handling/#remote-image-server-lxd-or-simplestreams)
that serves images generated by LXD. Instead of statically generating and serving
`index.json` and `images.json`, this service generates them on the fly. Since
it uses the same protocol as Canonicals image server it works with all LXD
features like `lxc launch` and auto-update.

### SSL
Since LXD only supports SSL servers you have generate a self-signed certificate:
```bash
openssl req -x509 -subj "/C=DE/CN=lxdocker.lxd" -addext "subjectAltName = DNS:lxdocker.lxd" -addext "keyUsage = critical,nonRepudiation,digitalSignature,keyEncipherment,keyAgreement" -addext "extendedKeyUsage = serverAuth,clientAuth" -newkey rsa:4096 -keyout key.pem -out cert.pem -sha512 -days 365 -nodes
```

If you call your lxdocker container `lxdocker`, then `lxdocker.lxd` can be
resolved using LXDs DNS server that runs on `lxdbr0`. If you add the bridge IP
as a secondary DNS server, LXD will be able to resolve it.
Alternatively you can add a static entry to `/etc/hosts`.

### CLI options:

#### `--address ADDRESS` (optional, default: ":443")
Address in the format `IP:port` that imgserver should listen to.
Defaults to `:443`.

#### `--lxdimages PATH` (required)
Path to the directory where `lxdocker` puts generated images.

#### `--key PATH` (required)
Path to the TLS key used by the server.

#### `--cert PATH` (required)
Path to the TLS certificate used by the server.
