# fnos-mfs

`fnos-mfs` 是给 fnOS 上的 mergerfs 媒体目录准备的交互式命令行工具。

它不走一堆参数。直接运行：

```bash
sudo fnos-mfs
```

然后按提示选择：

```text
1. App：fnvideo / fnmusic / fnxunlei / fnaria2
2. 操作：set / discover / acl / status / install
3. set 时从系统发现 /vol1 /vol2 /vol3
4. 用复选输入选择参与聚合的卷
5. 自动从 /volX/1000 这类目录推断 owner
6. 自动按 App 配置推断默认底层目录名和聚合入口路径
```

## 当前流程

启动后的第一步固定是选择 App：

```text
fnvideo
fnmusic
fnxunlei
fnaria2
```

第二步选择操作：

```text
set       配置 MFS 聚合目录
discover  只发现当前 /vol 卷
acl       给当前 App 补 ACL
status    查看当前 App 状态
install   安装 mergerfs/fuse3/acl
exit      退出
```

`set` 会做这些事：

```text
1. 发现 /vol1 /vol2 /vol3 这类卷
2. 复选参与聚合的卷，输入格式是 1,2,3 或 all
3. 从 /volX/1000 这类目录推断 owner
4. 按 App 配置给出默认底层目录名
5. 按 App 配置给出默认聚合入口路径
6. 显示执行计划
7. 输入 yes 后才真正创建目录、写 ACL、写 systemd、启动服务
```

默认 mergerfs 策略：

```text
category.create=mfs
moveonenospc=true
minfreespace=10G
allow_other
umask=000
```

## 配置

默认 App 配置在：

```text
configs/apps.json
```

运行时可以用下面的文件覆盖内置配置：

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

配置里主要有：

```text
default_pool_name  每个卷下面创建的隐藏底层目录
default_mount_dir  聚合入口目录名
path_template      默认入口路径模板
user_candidates    App Linux 用户候选名
service_name       systemd 服务名
```

这里用 JSON，不用 YAML。原因是 Go 标准库可以直接读 JSON，最后可以编译成单文件工具，不需要额外依赖。

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
聚合入口路径，比如 /vol1/1000/影视文件合集
```

`acl` 会给 App 用户补：

```text
父目录 --x
入口目录 rwx + default rwx
底层目录 rwx + default rwx
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

本机开发时运行：

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
复选输入解析
/volX 发现排序和元数据读取
owner 推断
默认路径渲染
setup 计划生成
ACL 父路径计算
systemd service 渲染和路径转义
```
