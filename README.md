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

## 构建

```bash
go build -o fnos-mfs .
```

拷到 fnOS 后：

```bash
chmod +x fnos-mfs
sudo ./fnos-mfs
```
