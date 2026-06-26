# fnos-mfs

[English](README.md) | 中文

`fnos-mfs` 是给 fnOS 上的 mergerfs 媒体目录准备的交互式命令行工具。

它的目标不是替代 fnOS 的存储管理，而是把多个独立 Basic 存储空间，通过 mergerfs 聚合成一个 App 友好的入口目录。

直接运行，不需要参数：

```bash
sudo fnos-mfs
```

## MFS 理念

推荐模型是：

```text
硬盘 A -> fnOS Basic 存储空间 -> /vol1
硬盘 B -> fnOS Basic 存储空间 -> /vol2
硬盘 C -> fnOS Basic 存储空间 -> /vol3

/vol1/1000/.media_pool
/vol2/1000/.media_pool
/vol3/1000/.media_pool
        |
        v
/vol1/1000/影视聚合
```

核心原则：

```text
每块硬盘都是 Basic
每块硬盘在 fnOS 里单独创建一个存储空间
不要为了媒体库把几块盘做成 RAID/JBOD
fnos-mfs 只负责把每块盘里的隐藏目录聚合给 App 看
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

默认写入策略是 mergerfs 的 `category.create=mfs`。新文件会写到当前剩余空间最多的那块盘，不是轮询，也不是同时写多块盘。

## 交互流程

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

## 内置 App 预设

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
聚合入口名：影视聚合
App 用户候选：trim.media, trim-media
```

配置字段：

```text
default_pool_name  每个卷下面创建的隐藏底层目录
default_mount_dir  聚合入口目录名
path_template      默认入口路径模板
user_candidates    App Linux 用户候选名
service_name       systemd 服务名
```

这里用 JSON，不用 YAML。原因是 Go 标准库可以直接读 JSON，最后可以编译成单文件工具，不需要额外依赖。

## set 做什么

`set` 是核心流程：

```text
1. 发现 /vol1 /vol2 /vol3 这类卷
2. 复选参与聚合的卷，输入格式是 1,2,3 或 all
3. 从 /volX/1000 这类目录推断 owner
4. 按 App 预设推断默认 App 用户
5. 给出默认底层目录名和聚合入口路径
6. 可选修改 appuser/name/path
7. 显示执行计划
8. 执行预检
9. 输入 yes 或 y 后才真正创建目录、写 ACL、写 systemd、启动服务
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
category.create=mfs
moveonenospc=true
minfreespace=10G
allow_other
umask=000
```

## 写入位置

工具会写这些系统位置：

```text
/etc/fnos-mfs/<app>.json
/etc/systemd/system/<service_name>.service
/etc/fuse.conf
```

会修改这些目录：

```text
每个选中卷的 /volX/<home>/<pool_name>
聚合入口路径，比如 /vol1/1000/影视聚合
```

ACL 策略：

```text
父目录：--x
入口目录：rwx + default rwx
底层目录：rwx + default rwx
```

## 错误处理

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

## 构建

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

拷到 fnOS 后：

```bash
chmod +x fnos-mfs
sudo ./fnos-mfs
```

## 测试

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

## Release

仓库包含一个手动触发的 prerelease workflow：

```text
.github/workflows/prerelease.yml
```

在 GitHub Actions 里运行 `Prerelease`，输入 tag，比如：

```text
v0.1.0-dev
```

workflow 会执行测试，构建 `fnos-mfs-linux-amd64`，并创建或更新 GitHub prerelease。

## 免责声明

本工具会修改系统挂载、ACL、FUSE 和 systemd 配置。使用前请自行确认数据备份和命令影响，先在非关键数据上测试。作者不对数据丢失、服务中断或系统配置损坏负责。

## License

MIT License. See [LICENSE](LICENSE).
