# HD2 Debian 12 容器部署

此目录的文件搭配 Linux amd64 的 `hd2_bot` 和同版本的 `playwright` 二进制使用。发布包已包含两者。
宿主机可继续使用 Debian 11；Chromium 和系统依赖安装在 Debian 12 镜像内，不使用宿主机的浏览器缓存。

## 从现有部署迁移

将 `hd2_bot-linux-amd64-docker.tar.gz` 上传到现有的 `~/hd` 目录。
先用原来的方式停止 HD2 进程（如果由 systemd、Supervisor 等管理，应停止对应服务，避免自动重启）。
不要停止明日方舟机器人。两个 HD2 实例不能同时使用同一个 token 和数据库。

```bash
cd ~/hd
# 停止旧进程后备份配置和状态；首次部署没有 data/ 时先创建。
mkdir -p data
tar -czf "hd2-backup-$(date +%Y%m%d-%H%M%S).tar.gz" hd2.yaml data
tar -xzf hd2_bot-linux-amd64-docker.tar.gz
docker compose config --quiet
docker compose up -d --build
docker compose logs -f --tail=100 hd2
```

需要 Docker Engine 和 Docker Compose v2。若 `docker compose version` 报错，先安装 Compose v2 插件。
第一次构建会联网下载 Chromium、驱动和 Debian 系统库，需要等待。
日志出现机器人启动成功后，可在群里用 `/help` 或 `/planets` 检查出图。
按 Ctrl+C 只退出日志查看，不会停止容器。

发布包不包含真实 `hd2.yaml` 或 `data/`，解压不会覆盖它们。
配置只读挂载、状态目录可写挂载；删除或重建容器不会删除宿主机里的这两项。
使用 Linux host 网络，原来的 `127.0.0.1` 接口地址和 HTTP 端口保持有效，需确保旧 HD2 已停止、端口未被其他程序占用。
配置中的 `photo_del_delay` 和 `inline.prefix` 已移除，可删除旧配置项。

## 日常操作

```bash
cd ~/hd
docker compose logs --tail=100 hd2
docker compose restart hd2
docker compose stop hd2
```

更新二进制后执行 `docker compose up -d --build`；仅修改配置后执行 `docker compose restart hd2`。
如需回退，先 `docker compose down`，再恢复旧二进制并按原方式启动。

## 构建说明

在源码根目录生成 Linux 二进制后，将它们与本目录文件放在同一目录：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o hd2_bot .
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o playwright github.com/mxschmitt/playwright-go/cmd/playwright
```

Playwright Go 版本锁定在项目 `go.mod` 中。发布包内的安装工具和主程序须使用同一版本。
