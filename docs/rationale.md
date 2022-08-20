# Rationale

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
- Docker is ineffecient by default. port-forwardings use a proxy server and the
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

## Why use lxdocker instead of native LXD images?
- some software like [PhotoPrism](https://docs.photoprism.app/getting-started/#setup) is only available as docker images
- It uses less resources. Even Alpine runs many services via openrc.
