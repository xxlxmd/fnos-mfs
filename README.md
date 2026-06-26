# fnos-mfs

English | [中文](README-cn.md)

`fnos-mfs` is an interactive CLI for building app-friendly mergerfs media directories on fnOS.

It is designed for a simple storage model: keep every physical disk as its own fnOS Basic storage space, then expose one merged directory to apps such as fnOS Video, fnOS Music, Xunlei, or Aria2.

It does not do magic; it just makes path wrangling less dramatic. 🧭

Run it from the directory that contains the binary:

```bash
sudo ./fnos-mfs
```

The default interactive language is Chinese. Use `-en` for English prompts:

```bash
sudo ./fnos-mfs -en
```

Show built-in help:

```bash
./fnos-mfs -h
./fnos-mfs -help
./fnos-mfs --help
```

`fnos-mfs` is interactive. Normal use does not require any other arguments.

## 🧭 Storage Philosophy

The intended model is:

```text
Disk A -> fnOS Basic storage space -> /vol1
Disk B -> fnOS Basic storage space -> /vol2
Disk C -> fnOS Basic storage space -> /vol3

/vol1/1000/mfs_pools/.media_pool
/vol2/1000/mfs_pools/.media_pool
/vol3/1000/mfs_pools/.media_pool
        |
        v
/vol1/1000/影视聚合
```

`fnos-mfs` does not try to replace fnOS storage management. It assumes fnOS has already created and mounted each disk as a separate Basic storage space.

The tool then:

```text
1. discovers /vol1, /vol2, /vol3...
2. creates a hidden branch directory under mfs_pools on each selected volume
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

By default, new files use mergerfs `category.create=mfs`, meaning new files are created on the selected disk with the most free space. You can also choose `pfrd` during `set` so mixed-size disks are used closer to their capacity ratio.

## 🖥️ Interactive Flow

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
Branch root directory
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

## 📦 Built-In App Presets

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
branch root:      mfs_pools
mount directory:  影视聚合
app user candidates: trim.media, trim-media
```

Config fields:

```text
default_pool_name  hidden branch directory created under mfs_pools on every selected volume
default_mount_dir  visible merged directory name
path_template      default mount path template
user_candidates    Linux app user candidates
service_name       systemd service name
```

JSON is used instead of YAML so the Go binary can stay dependency-free.

## 🛠️ What `set` Does

`set` is the main workflow:

```text
1. discover /vol1 /vol2 /vol3...
2. select the volumes to merge, for example 1,2,3 or all
3. infer owner from /volX/1000-style home directories
4. infer the default app user from the selected preset
5. propose default branch and mount paths
6. choose or update the create policy, for example mfs or pfrd
7. show the execution plan
8. run preflight checks
9. only apply changes after you confirm the final set action
```

Existing setups can be updated by running `set` again. For example, changing the create policy from `mfs` to `pfrd` does not require deleting directories; the tool rewrites the state file and systemd service, then restarts the current service.

Confirmation prompts are explicit:

```text
yes/y        perform the action named in the prompt
no/n/Enter   cancel or keep the current defaults
other input  invalid; the tool asks again
```

Preflight checks catch common mistakes before touching the system:

```text
selected volume count
whether selected /volX paths are mounted
duplicate volume or branch paths
valid branch directory name, without / or :
absolute mount path
app user existence
owner UID/GID availability
mount point conflicts with branch directories
mounted mount point conflicts
```

Default mergerfs options:

```text
category.create=mfs or pfrd
moveonenospc=true
minfreespace=10G
allow_other
umask=000
```

## 🧠 MFS Write Behavior

Upstream mergerfs resources:

```text
Official documentation: https://trapexit.github.io/mergerfs/
Open source repository: https://github.com/trapexit/mergerfs
```

`fnos-mfs` is an interactive configuration helper around mergerfs. It is not mergerfs itself and does not modify the upstream mergerfs project.

Two create policies are supported:

```text
mfs   most free space; choose the disk with the most free space in absolute bytes
pfrd  percentage free random distribution; random distribution by free-space percentage
```

`mfs` is useful when you add a large empty disk and want new files to flow there first. `pfrd` is better for mixed-size disks such as 500G, 500G, and 1000G when you want long-term usage closer to capacity ratio.

Run `set` again to update the policy. Existing files are not moved automatically; the new policy only affects newly created files.

Important behavior:

```text
It compares free GB/TB, not usage percentage
It affects new files only
It does not move or rebalance existing files
Reads show files from all selected branches as one directory
Deletes remove the real file from the branch where that file lives
Branches with less than minfreespace=10G are avoided for new files
moveonenospc=true lets mergerfs retry another branch if the chosen branch runs out of space during a write
```

Example with three empty disks:

```text
Disk A: 500G free
Disk B: 1000G free
Disk C: 2000G free
```

With `mfs`, new files first go to Disk C because it has the most free space. When Disk C drops to around 1000G free, Disk B can also become a target. Disk A usually starts receiving new files only after the larger disks drop to around 500G free.

With `pfrd`, all three empty disks start at 100% free, so writes do not keep targeting only the 2000G disk. Long term, usage trends closer to capacity ratio: 500G, 1000G, and 2000G roughly lean toward 1:2:4, not equal file counts.

Example when adding a new disk:

```text
Existing Disk A: 200G free
Existing Disk B: 180G free
New Disk C:      2000G free
```

After you run `set` again and include Disk C, old files stay on Disk A and Disk B. New files will mostly go to Disk C until its free space gets close to the older disks. This is normal. MFS does not automatically redistribute existing media.

Example when old disks are almost full:

```text
Disk A: 80G free
Disk B: 60G free
New Disk C: 2000G free
```

Most new writes go to Disk C. If Disk A or Disk B drops below `minfreespace=10G`, mergerfs avoids those branches for new files. Existing files on those disks are still visible through the merged directory.

Practical notes:

```text
For media libraries, this usually means new downloads naturally flow to the emptiest disk
Adding a large empty disk is useful when old disks are near full
If you want old files redistributed, move them manually while the service is stopped and you understand the real branch paths
Do not delete hidden pool directories to "clean up"; they may contain the real files
```

## 📍 Files Written

System files:

```text
/etc/fnos-mfs/<app>.json
/etc/systemd/system/<service_name>.service
/etc/fuse.conf
```

Storage paths:

```text
/volX/<home>/mfs_pools/<pool_name>
/volX/<home>/mfs_pools/.readme.txt
merged mount path, for example /vol1/1000/影视聚合
```

`mfs_pools/.readme.txt` is written on every selected volume. It explains in Chinese and English that the hidden pool directories are real mergerfs branch directories and should not be deleted, moved, or renamed while the service is in use.

Note: the interactive "branch directory name" field should contain only the hidden directory itself, for example `.media_pool`. Do not enter `mfs_pools/.media_pool`. If you enter `mfs_pools/.media_pool`, the tool detects the prefix and uses `.media_pool`.

When you re-enter a custom app, if `/etc/fnos-mfs/<app>.json` already exists, the tool reads the old config and uses the saved app user, branch directory name, merged mount name, and systemd service name as defaults. This keeps older custom apps such as `music` or `video` from falling back to `.mfs_pool`.

At startup, the tool also scans `/etc/fnos-mfs/*.json`. Saved custom apps are automatically appended to the first app list; built-in apps still come from the app config and are not duplicated by old state files.

ACL policy:

```text
parent directories: --x
visible mount path: rwx + default rwx
hidden branch paths: rwx + default rwx
```

## 🧳 Upgrade Migration

This applies when an older version already created branch directories. Older paths usually look like:

```text
/volX/1000/.media_pool
/volX/1000/.music_pool
```

Newer paths are stored under `mfs_pools`:

```text
/volX/1000/mfs_pools/.media_pool
/volX/1000/mfs_pools/.music_pool
```

Recommended migration flow:

```text
1. Replace the binary with the new fnos-mfs and confirm chmod 755 fnos-mfs
2. Run sudo ./fnos-mfs
3. Choose the original app on the first screen; saved custom apps appear automatically
4. If a custom app does not appear, choose other and enter the original app ID, for example music
5. Choose set
6. Select the volumes that already participate in MFS; you may also add a new volume
7. For branch directory name, enter only .media_pool or .music_pool; do not enter mfs_pools/.media_pool
8. Review preflight checks, then run set if there are no red conflicts
```

The tool handles these cases automatically:

```text
mfs_pools does not exist: create it
mfs_pools/.readme.txt does not exist: write the directory notice
old directory exists and new directory does not exist: move the old directory under mfs_pools
old directory exists and new directory is empty: remove the empty new directory, then move the old directory
mount path is already mounted by the current fnos-mfs service: show a yellow warning, then stop and restart that service during set
```

The tool does not auto-handle these cases:

```text
old and new directories both exist, and the new directory is not empty
old path is not a directory
new path is not a directory
```

In those cases, the tool stops to avoid overwriting real data. Check both directories manually, then decide which directory to keep or merge manually. Do not blindly delete old `.media_pool` or `.music_pool` directories; they may contain the real media files currently used by the app.

This may feel a little fussy, but the goal is simple: ask one extra question before accidentally "organizing" real files into oblivion. 🙂

Useful checks:

```bash
findmnt /vol13/1000/音乐聚合
systemctl status fnos-mfs-music --no-pager
ls -lah /vol13/1000/.music_pool
ls -lah /vol13/1000/mfs_pools/.music_pool
```

## 🧯 Error Handling

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

## 🏗️ Build

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

## 🚀 Run On fnOS

Save the release binary in a user-writable home directory, for example:

```text
/vol1/1000/command/fnos-mfs
```

You do not need to install it into `/usr/bin`, `/usr/local/bin`, or another system directory.

First enable SSH in fnOS. The path is usually:

```text
Settings -> Security -> SSH
```

Then connect from your local computer:

```bash
# Windows: PowerShell, Windows Terminal, PuTTY, Tabby, or another SSH client
ssh username@fnos-ip

# macOS: Terminal
ssh username@fnos-ip
```

Example:

```bash
ssh XHOME@192.168.1.20
```

After entering the fnOS user password, go to the directory where `fnos-mfs` is saved. If you downloaded or uploaded it through the fnOS file manager, open the file properties, copy the original path, then `cd` to that path over SSH:

```bash
cd /vol1/1000/command
```

If the path contains spaces, quote it:

```bash
cd "/vol1/1000/My Command"
```

Recommended direct download flow:

```bash
mkdir -p /vol1/1000/command
cd /vol1/1000/command

curl -L -o fnos-mfs \
  https://github.com/xxlxmd/fnos-mfs/releases/download/v0.1.3-dev/fnos-mfs

chmod 755 fnos-mfs
sudo ./fnos-mfs
```

For English interactive text:

```bash
sudo ./fnos-mfs -en
```

For help:

```bash
./fnos-mfs -h
```

Use `./fnos-mfs` because Linux does not search the current directory by default. Running it without `./` only works after installing the binary into a directory in `PATH`, which is not required for this tool.

You can also enter a root shell first:

```bash
sudo -i
cd /vol1/1000/command
./fnos-mfs
```

Note that `sudo -i` switches to the root environment, so you still need to `cd` back to the directory containing `fnos-mfs`.

If you copied the binary by another method, still make sure it has the executable bit:

```bash
cd /vol1/1000/command
ls -lah fnos-mfs
chmod 755 fnos-mfs
sudo ./fnos-mfs
```

If `sudo ./fnos-mfs` says `command not found`, the file is usually not in the current directory or has a different name:

```bash
pwd
ls -lah
```

If it says `permission denied`, check the binary and parent directory permissions:

```bash
namei -l /vol1/1000/command/fnos-mfs
chmod 755 /vol1/1000/command/fnos-mfs
```

## 🧪 Test

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

## 🎁 Release

The repository includes a manual prerelease workflow:

```text
.github/workflows/prerelease.yml
```

Run `Prerelease` in GitHub Actions with a tag such as:

```text
v0.1.3-dev
```

The workflow runs tests, builds `fnos-mfs`, and creates or updates a GitHub prerelease. Release notes read commit subjects and sort updates into features, fixes, docs, and other changes in both Chinese and English, with a little emoji sparkle. 🎉

The classifier is intentionally plain:

```text
Features: feat/feature prefix, or subjects containing 新增, 增加, 支持, 自动
Fixes: fix prefix, or subjects containing bug, 修复, 修正, 报错, 失败
Docs: docs/doc prefix, or subjects containing README, 文档
Other: everything else goes into the toolbox before we make dramatic assumptions
```

A commit can appear in multiple sections. For example, a subject with both `feat` and `修复` lands in both Features and Fixes. That is not duplication; it is the release note robot reading the room.

## ⚠️ Disclaimer

`fnos-mfs` is an independent third-party open source project. It is not affiliated with, authorized by, endorsed by, or officially connected to the fnOS project, 飞牛, 飞牛 NAS, 飞牛私有云, their companies, products, trademarks, or official services.

This tool modifies system mounts, ACLs, FUSE, and systemd configuration. Review the execution plan, keep backups, and test on non-critical data first. The authors are not responsible for data loss, service interruption, or system configuration damage.

## 📄 License

MIT License. See [LICENSE](LICENSE).
