# fnos-mfs

[English](README.md) | 中文

`fnos-mfs` 是给 fnOS 上的 mergerfs 媒体目录准备的交互式命令行工具。

它的目标不是替代 fnOS 的存储管理，而是把多个独立 Basic 存储空间，通过 mergerfs 聚合成一个 App 友好的入口目录。

它不负责变魔法，主要负责少让你在三块盘之间来回找目录。🧭

在二进制文件所在目录直接运行：

```bash
sudo ./fnos-mfs
```

默认交互语言是中文。如果需要英文提示，使用 `-en`：

```bash
sudo ./fnos-mfs -en
```

查看内置帮助：

```bash
./fnos-mfs -h
./fnos-mfs -help
./fnos-mfs --help
```

`fnos-mfs` 是交互式工具，正常使用不需要其他参数。

## 🧭 MFS 理念

推荐模型是：

```text
硬盘 A -> fnOS Basic 存储空间 -> /vol1
硬盘 B -> fnOS Basic 存储空间 -> /vol2
硬盘 C -> fnOS Basic 存储空间 -> /vol3

/vol1/1000/mfs_pools/.media_pool
/vol2/1000/mfs_pools/.media_pool
/vol3/1000/mfs_pools/.media_pool
        |
        v
/vol1/1000/影视聚合
```

核心原则：

```text
每块硬盘都是 Basic
每块硬盘在 fnOS 里单独创建一个存储空间
不要为了媒体库把几块盘做成 RAID/JBOD
fnos-mfs 只负责把每块盘 mfs_pools 里的隐藏目录聚合给 App 看
```

这样做的好处：

```text
每块盘仍然可以单独读取
不会因为一个池损坏导致所有盘一起不可读
影视、音乐、下载软件只看到一个统一目录
冷数据盘在没有扫描和访问时更容易休眠
扩容时只需要增加新的 Basic 存储空间，再加入 MFS
```

它不是：

```text
不是备份
不是 RAID
不会自动复制两份
不会让普通硬盘获得冗余保护
```

默认写入策略是 mergerfs 的 `category.create=mfs`。新文件会写到当前剩余空间最多的那块盘，不是轮询，也不是同时写多块盘。也可以在 `set` 里改成 `pfrd`，让大小不同的盘更接近按容量比例使用。

## 🖥️ 交互流程

第一步选择 App：

```text
fnvideo
fnmusic
fnxunlei
fnaria2
other
```

选择内置 App 后，会先显示状态面板：

```text
App 用户是否存在
默认底层目录名
底层根目录
默认聚合入口名
root/systemd/apt 状态
mergerfs/fuse3/acl 安装状态
已保存配置
systemd 服务状态
当前发现的 /volX 卷
```

颜色含义：

```text
绿色  正常
黄色  未配置或当前环境无法确认
红色  缺失或不安全
```

然后选择操作：

```text
set       配置或修改 MFS 聚合目录
discover  只发现当前 /vol 卷
acl       给当前 App 重新补 ACL
status    查看保存配置和运行状态
install   安装 mergerfs/fuse3/acl
exit      退出
```

菜单会持续运行。`status`、`discover` 或一次失败的 `set` 不会直接退出，只有选择 `exit` 才退出。

## 📦 内置 App 预设

默认 App 配置内置在：

```text
configs/apps.json
```

运行时可以用下面的文件覆盖：

```text
/etc/fnos-mfs/apps.json
```

当前内置 App：

```text
fnvideo  -> 飞牛影视
fnmusic  -> 飞牛音乐
fnxunlei -> 飞牛迅雷
fnaria2  -> Aria2
```

`fnvideo` 默认：

```text
底层目录名：.media_pool
底层根目录：mfs_pools
聚合入口名：影视聚合
App 用户候选：trim.media, trim-media
```

配置字段：

```text
default_pool_name  每个卷的 mfs_pools 下面创建的隐藏底层目录
default_mount_dir  聚合入口目录名
path_template      默认入口路径模板
user_candidates    App Linux 用户候选名
service_name       systemd 服务名
```

这里用 JSON，不用 YAML。原因是 Go 标准库可以直接读 JSON，最后可以编译成单文件工具，不需要额外依赖。

## 🛠️ set 做什么

`set` 是核心流程：

```text
1. 发现 /vol1 /vol2 /vol3 这类卷
2. 复选参与聚合的卷，输入格式是 1,2,3 或 all
3. 从 /volX/1000 这类目录推断 owner
4. 按 App 预设推断默认 App 用户
5. 给出默认底层目录名和聚合入口路径
6. 选择或更新写入策略，例如 mfs 或 pfrd
7. 显示执行计划
8. 执行预检
9. 最后确认执行 set 后才真正创建目录、写 ACL、写 systemd、启动服务
```

已经创建过的配置也可以重新执行 `set` 来更新。比如只想把写入策略从 `mfs` 改成 `pfrd`，不需要删除目录；工具会重写状态文件和 systemd 服务，然后重启当前服务。

确认提示的含义是固定的：

```text
yes/y        执行提示里写的动作
no/n/回车    取消，或者保留当前默认值
其他输入      无效，会重新询问
```

预检会提前发现这些问题：

```text
选择卷数量不足
/volX 目录存在但没有真正挂载
重复卷或重复底层目录
底层目录名包含 / 或 :
聚合入口不是绝对路径
App 用户不存在
owner UID/GID 缺失
聚合入口和底层目录互相包含
聚合入口已经被其他挂载占用
```

默认 mergerfs 策略：

```text
category.create=mfs 或 pfrd
moveonenospc=true
minfreespace=10G
allow_other
umask=000
```

## 🧠 MFS 写入规则

上游 mergerfs 资料：

```text
官网/文档：https://trapexit.github.io/mergerfs/
开源地址：https://github.com/trapexit/mergerfs
```

`fnos-mfs` 是围绕 mergerfs 做交互式配置的辅助工具。它不是 mergerfs 本体，也不会修改上游 mergerfs 项目。

支持两种写入策略：

```text
mfs   most free space，写到剩余空间绝对值最多的盘
pfrd  percentage free random distribution，按剩余空间百分比做随机分布
```

`mfs` 适合新增一块大空盘后，让新文件优先流向大空盘。`pfrd` 更适合 500G、500G、1000G 这类大小不一致的盘，长期看更接近按容量比例使用。

重新执行 `set` 可以更新策略。旧文件不会自动移动，新策略只影响之后新创建的文件。

几个核心特性：

```text
比较的是剩余 GB/TB，不是使用百分比
只影响新文件
不会自动移动或重新平衡旧文件
读取时会把所有选中底层目录合成一个目录给 App 看
删除文件时，会删除这个文件真实所在底层目录里的文件
低于 minfreespace=10G 的底层目录，会尽量不再写入新文件
moveonenospc=true 表示写入中途遇到空间不足时，会尝试换另一个底层目录继续写
```

例如 3 块空盘：

```text
硬盘 A：500G 可用
硬盘 B：1000G 可用
硬盘 C：2000G 可用
```

使用 `mfs` 时，新文件会先写到硬盘 C，因为它剩余空间最多。当硬盘 C 降到接近 1000G 可用时，硬盘 B 也可能开始被选中。当大盘都降到接近 500G 可用时，硬盘 A 才会更常被选中。

使用 `pfrd` 时，三块空盘一开始都是 100% free，所以不会一直盯着 2000G 那块盘写。长期看更接近容量比例，500G、1000G、2000G 大概会倾向 1:2:4，而不是文件数量平均。

例如原来有 2 块盘，后来新增 1 块空盘：

```text
原硬盘 A：200G 可用
原硬盘 B：180G 可用
新硬盘 C：2000G 可用
```

重新执行 `set` 并把新硬盘 C 加入后，旧文件不会自动搬家。之后新文件会大部分写到硬盘 C，直到硬盘 C 的剩余空间接近旧盘。这个是正常行为。MFS 不是自动均衡工具。

例如旧盘已经快满，或者超过 80%：

```text
硬盘 A：80G 可用
硬盘 B：60G 可用
新硬盘 C：2000G 可用
```

新增空盘后，大部分新写入都会进入硬盘 C。如果硬盘 A 或 B 低于 `minfreespace=10G`，mergerfs 会尽量避开它们，不再给它们写新文件。旧盘上的原文件仍然会通过聚合目录显示出来。

实际使用建议：

```text
影视、音乐、下载这类媒体库，新文件会自然流向最空的盘
旧盘快满时，新增一块大空盘很适合这个规则
如果你想重新分布旧文件，需要停止服务后手动移动，并确认真实底层路径
不要为了清理目录删除隐藏池目录，里面可能就是真实文件
```

## 📍 写入位置

工具会写这些系统位置：

```text
/etc/fnos-mfs/<app>.json
/etc/systemd/system/<service_name>.service
/etc/fuse.conf
```

会修改这些目录：

```text
每个选中卷的 /volX/<home>/mfs_pools/<pool_name>
每个选中卷的 /volX/<home>/mfs_pools/.readme.txt
聚合入口路径，比如 /vol1/1000/影视聚合
```

`mfs_pools/.readme.txt` 会写到每个选中的卷里。里面用中文和英文说明当前目录是 fnos-mfs 的真实底层池目录，隐藏目录不要在服务运行时删除、移动或重命名。

注意：交互里的“底层目录名”只填隐藏目录本身，比如 `.media_pool`。不要填 `mfs_pools/.media_pool`。如果误填了 `mfs_pools/.media_pool`，工具会自动识别并改成 `.media_pool`。

自定义 App 重新进入时，如果 `/etc/fnos-mfs/<app>.json` 已经存在，工具会读取旧配置，并把旧的 App 用户、底层目录名、聚合入口名、systemd 服务名作为默认值。这样旧的自定义 `music`、`video` 这类配置不会退回 `.mfs_pool`。

程序启动时也会扫描 `/etc/fnos-mfs/*.json`。保存过的自定义 App 会自动追加到第一步 App 列表里；内置 App 仍然以配置文件为准，不会因为旧状态文件重复出现。

ACL 策略：

```text
父目录：--x
入口目录：rwx + default rwx
底层目录：rwx + default rwx
```

## 🧳 升级迁移

适用于旧版本已经创建过底层目录的情况。旧版本目录通常是：

```text
/volX/1000/.media_pool
/volX/1000/.music_pool
```

新版目录会统一放到 `mfs_pools` 下面：

```text
/volX/1000/mfs_pools/.media_pool
/volX/1000/mfs_pools/.music_pool
```

推荐迁移流程：

```text
1. 换成新版 fnos-mfs 二进制，并确认 chmod 755 fnos-mfs
2. 执行 sudo ./fnos-mfs
3. 第一屏选择原 App；保存过的自定义 App 会自动出现在列表里
4. 如果自定义 App 没出现，选择 other，然后输入原来的 App ID，例如 music
5. 选择 set
6. 选择原来参与 MFS 的卷，也可以同时加入新卷
7. 底层目录名只填 .media_pool、.music_pool 这类名字，不要填 mfs_pools/.media_pool
8. 看预检结果，确认没有红色冲突后执行 set
```

工具会自动处理这些情况：

```text
mfs_pools 不存在：自动创建
mfs_pools/.readme.txt 不存在：自动写入说明
旧目录存在，新目录不存在：把旧目录移动到 mfs_pools 下面
旧目录存在，新目录为空：删除空的新目录，再把旧目录移动过去
挂载入口已经由当前 fnos-mfs 服务挂载：显示黄色提示，执行 set 时会先停止旧服务再重启
```

工具不会自动处理这些情况：

```text
旧目录和新目录都存在，并且新目录非空
旧路径不是目录
新路径不是目录
```

遇到这些情况时，工具会停止，避免覆盖真实数据。你需要先手动确认两个目录里分别有什么文件，再决定保留哪一个或手动合并。不要直接删除旧的 `.media_pool`、`.music_pool`，里面可能就是 App 正在使用的真实媒体文件。

这一步看起来有点啰嗦，但它的核心目标很朴素：宁可多问一句，也不要帮你把真文件“整理没了”。🙂

可用这些命令辅助确认：

```bash
findmnt /vol13/1000/音乐聚合
systemctl status fnos-mfs-music --no-pager
ls -lah /vol13/1000/.music_pool
ls -lah /vol13/1000/mfs_pools/.music_pool
```

## 🧯 错误处理

遇到错误时，工具会输出：

```text
错误: 具体失败原因
修复建议:
 - 下一步处理方式
```

常见情况：

```bash
# 没有发现 /volX
ls -ld /vol*
findmnt | grep /vol

# 缺少依赖
apt update && apt install -y mergerfs fuse3 acl

# App 用户不存在
id <appuser>

# systemd 启动失败
systemctl status <service> --no-pager
journalctl -u <service> -n 100 --no-pager
```

## 🏗️ 构建

```bash
go build -o fnos-mfs .
```

或者：

```bash
make build
```

给常见 x86 fnOS 主机打包：

```bash
make linux-amd64
```

## 🚀 在 fnOS 上运行

建议把 release 文件保存到用户可写的 home 目录里，比如：

```text
/vol1/1000/command/fnos-mfs
```

不需要安装到 `/usr/bin`、`/usr/local/bin` 这类系统目录。

先在飞牛系统里打开 SSH。一般路径是：

```text
设置 -> 安全 -> SSH
```

然后在本地电脑连接飞牛：

```bash
# Windows 可以用 PowerShell、Windows Terminal、PuTTY、Tabby 等 SSH 工具
ssh 用户名@飞牛IP

# macOS 可以用“终端”
ssh 用户名@飞牛IP
```

例如：

```bash
ssh XHOME@192.168.1.20
```

输入飞牛用户密码后，进入你保存 `fnos-mfs` 的目录。如果你是在飞牛文件管理器里下载或上传的文件，可以右键/属性查看“原始路径”，复制后在 SSH 里 `cd` 进去：

```bash
cd /vol1/1000/command
```

如果路径里有空格，用引号包起来：

```bash
cd "/vol1/1000/My Command"
```

推荐直接在 SSH 里下载到固定目录：

```bash
mkdir -p /vol1/1000/command
cd /vol1/1000/command

curl -L -o fnos-mfs \
  https://github.com/xxlxmd/fnos-mfs/releases/download/v0.1.3-dev/fnos-mfs

chmod 755 fnos-mfs
sudo ./fnos-mfs
```

如果需要英文交互提示：

```bash
sudo ./fnos-mfs -en
```

查看帮助：

```bash
./fnos-mfs -h
```

这里要用 `./fnos-mfs`，因为 Linux 默认不会从当前目录查找命令。只有把文件安装到 `PATH` 里的目录后，不带 `./` 的写法才会工作；本工具不需要这样安装。

也可以先进入 root shell，再运行：

```bash
sudo -i
cd /vol1/1000/command
./fnos-mfs
```

注意：`sudo -i` 会切到 root 环境，所以仍然要先 `cd` 回 `fnos-mfs` 所在目录。

如果你是用别的方式拷贝过去，也要先给执行权限：

```bash
cd /vol1/1000/command
ls -lah fnos-mfs
chmod 755 fnos-mfs
sudo ./fnos-mfs
```

如果 `sudo ./fnos-mfs` 提示 `command not found`，通常是当前目录没有这个文件，或者文件名不一致：

```bash
pwd
ls -lah
```

如果提示 `permission denied`，检查文件和上级目录权限：

```bash
namei -l /vol1/1000/command/fnos-mfs
chmod 755 /vol1/1000/command/fnos-mfs
```

## 🧪 测试

```bash
go test ./...
go build -o fnos-mfs .
```

或者：

```bash
make check
```

单元测试覆盖：

```text
App JSON 配置校验
other 自定义 App
状态渲染
复选输入解析
/volX 发现排序和元数据读取
owner 推断
set 预检
默认路径渲染
systemd service 渲染和路径转义
错误修复建议
```

## 🎁 Release

仓库包含一个手动触发的 prerelease workflow：

```text
.github/workflows/prerelease.yml
```

在 GitHub Actions 里运行 `Prerelease`，输入 tag，比如：

```text
v0.1.3-dev
```

workflow 会执行测试，构建 `fnos-mfs`，并创建或更新 GitHub prerelease。Release notes 会读取提交内容，自动分成新功能、修复、文档和其他更新，中英文都有，还会带一点 emoji。🎉

分类规则很朴素：

```text
新功能：feat/feature 开头，或提交里有 新增、增加、支持、自动
修复：fix 开头，或提交里有 bug、修复、修正、报错、失败
文档：docs/doc 开头，或提交里有 README、文档
其他：剩下的都先进工具箱，先不冤枉任何一个 commit
```

一个 commit 可以同时进入多个分类。比如标题里既有 `feat` 又有 `修复`，它会同时出现在新功能和修复里。这个不算重复，是工具在认真阅读空气里的暗示。

## ⚠️ 免责声明

`fnos-mfs` 是独立的第三方开源项目。它与 fnOS 项目、飞牛、飞牛 NAS、飞牛私有云、相关公司、产品、商标或官方服务没有隶属、授权、背书或官方合作关系。

本工具会修改系统挂载、ACL、FUSE 和 systemd 配置。使用前请自行确认数据备份和命令影响，先在非关键数据上测试。作者不对数据丢失、服务中断或系统配置损坏负责。

## 📄 License

MIT License. See [LICENSE](LICENSE).
