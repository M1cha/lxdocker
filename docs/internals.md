# Internals

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
running the `lxc` CLI are all the same system. This is also what the rest of
this README assumes.
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
  won't be overwritten so you can use LXDs image configuration to change their
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
