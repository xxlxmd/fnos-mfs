# fnos-mfs

English | [中文](README-cn.md)

`fnos-mfs` is an interactive CLI for building app-friendly mergerfs media directories on fnOS.

It is designed for a simple storage model: keep every physical disk as its own fnOS Basic storage space, then expose one merged directory to apps such as fnOS Video, fnOS Music, Xunlei, or Aria2.

Run it without flags:

```bash
sudo fnos-mfs
```

## Storage Philosophy

The intended model is:

```text
Disk A -> fnOS Basic storage space -> /vol1
Disk B -> fnOS Basic storage space -> /vol2
Disk C -> fnOS Basic storage space -> /vol3

/vol1/1000/.media_pool
/vol2/1000/.media_pool
/vol3/1000/.media_pool
        |
        v
/vol1/1000/影视聚合
```

`fnos-mfs` does not try to replace fnOS storage management. It assumes fnOS has already created and mounted each disk as a separate Basic storage space.

The tool then:

```text
1. discovers /vol1, /vol2, /vol3...
2. creates a hidden branch directory on each selected volume
3. mounts those branch directories into one visible app directory
4. grants the selected app user access to both the visible mount and the hidden branches
5. writes a systemd service so the merge survives reboot
```

Why this model:

```text
Each disk remains independently readable
No RAID/JBOD coupling
One disk failure does not make all disks unreadable
Apps see one clean library path
Cold media disks can still sleep when apps do not scan them
```

What it is not:

```text
It is not backup
It is not RAID
It does not duplicate files across disks
It does not make different disks as safe as replicated storage
```

By default, new files use mergerfs `category.create=mfs`, meaning new files are created on the selected disk with the most free space.

## Interactive Flow

The first screen asks you to choose an app:

```text
fnvideo
fnmusic
fnxunlei
fnaria2
other
```

For built-in apps, the tool shows a status dashboard before the action menu:

```text
App user existence
Default branch directory name
Default merged mount name
root/systemd/apt status
mergerfs/fuse3/acl installation status
saved fnos-mfs config
systemd service state
discovered /volX volumes
```

Colors:

```text
green   OK
yellow  warning or not configured
red     missing or unsafe
```

Then choose an action:

```text
set       create or update an MFS merge
discover  show discovered /volX volumes
acl       re-apply app ACL
status    show saved config and runtime checks
install   install mergerfs/fuse3/acl
exit      quit
```

The menu stays open after `status`, `discover`, or a failed `set`. It only exits when you choose `exit`.

## Built-In App Presets

The default app config is embedded from:

```text
configs/apps.json
```

At runtime, it can be overridden by:

```text
/etc/fnos-mfs/apps.json
```

Built-in presets:

```text
fnvideo  -> fnOS Video
fnmusic  -> fnOS Music
fnxunlei -> fnOS Xunlei
fnaria2  -> Aria2
```

`fnvideo` defaults:

```text
branch directory: .media_pool
mount directory:  影视聚合
app user candidates: trim.media, trim-media
```

Config fields:

```text
default_pool_name  hidden branch directory created on every selected volume
default_mount_dir  visible merged directory name
path_template      default mount path template
user_candidates    Linux app user candidates
service_name       systemd service name
```

JSON is used instead of YAML so the Go binary can stay dependency-free.

## What `set` Does

`set` is the main workflow:

```text
1. discover /vol1 /vol2 /vol3...
2. select the volumes to merge, for example 1,2,3 or all
3. infer owner from /volX/1000-style home directories
4. infer the default app user from the selected preset
5. propose default branch and mount paths
6. optionally edit app user, branch directory name, or mount path
7. show the execution plan
8. run preflight checks
9. only apply changes after you type yes or y
```

Preflight checks catch common mistakes before touching the system:

```text
selected volume count
whether selected /volX paths are mounted
duplicate volume or branch paths
valid branch directory name
absolute mount path
app user existence
owner UID/GID availability
mount point conflicts with branch directories
```

Default mergerfs options:

```text
category.create=mfs
moveonenospc=true
minfreespace=10G
allow_other
umask=000
```

## Files Written

System files:

```text
/etc/fnos-mfs/<app>.json
/etc/systemd/system/<service_name>.service
/etc/fuse.conf
```

Storage paths:

```text
/volX/<home>/<pool_name>
merged mount path, for example /vol1/1000/影视聚合
```

ACL policy:

```text
parent directories: --x
visible mount path: rwx + default rwx
hidden branch paths: rwx + default rwx
```

## Error Handling

Errors include repair hints:

```text
错误: concrete failure
修复建议:
 - next step
```

Common fixes:

```bash
# no /volX discovered
ls -ld /vol*
findmnt | grep /vol

# missing dependencies
apt update && apt install -y mergerfs fuse3 acl

# app user missing
id <appuser>

# systemd failure
systemctl status <service> --no-pager
journalctl -u <service> -n 100 --no-pager
```

## Build

```bash
go build -o fnos-mfs .
```

or:

```bash
make build
```

Build a Linux amd64 binary for common x86 fnOS hosts:

```bash
make linux-amd64
```

Copy to fnOS and run:

```bash
chmod +x fnos-mfs
sudo ./fnos-mfs
```

## Test

```bash
go test ./...
go build -o fnos-mfs .
```

or:

```bash
make check
```

Tests cover:

```text
app JSON validation
custom app flow
status rendering
selection parsing
/volX discovery
owner inference
setup plan checks
path rendering
systemd unit rendering
error repair hints
```

## Release

The repository includes a manual prerelease workflow:

```text
.github/workflows/prerelease.yml
```

Run `Prerelease` in GitHub Actions with a tag such as:

```text
v0.1.0-dev
```

The workflow runs tests, builds `fnos-mfs-linux-amd64`, and creates or updates a GitHub prerelease.

## Disclaimer

This tool modifies system mounts, ACLs, FUSE, and systemd configuration. Review the execution plan, keep backups, and test on non-critical data first. The authors are not responsible for data loss, service interruption, or system configuration damage.

## License

MIT License. See [LICENSE](LICENSE).
