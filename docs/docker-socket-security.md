# 自托管导航页挂载 docker.sock 的安全问题

> 整理时间：2026-11-21
> 背景：给 JustNav（自建导航页）做 Docker 自动发现功能，需要读容器列表。
> 挂 `/var/run/docker.sock` 是否必要、风险多大、有没有更好的做法。
> 本文只陈述机制与可选方案，不做结论，供外部评估。

---

## 一、问题是什么

导航页想自动发现「现在有哪些容器在跑、哪个有端口映射」，这样用户不用手工一条条添加服务。

要拿到这个信息，必须访问 Docker 的列表接口。访问方式有两种：

```yaml
# 方式 A：直接把 socket 文件挂进容器
volumes:
  - /var/run/docker.sock:/var/run/docker.sock

# 方式 B：通过 TCP 连一个代理
environment:
  DOCKER_HOST: tcp://socket-proxy:2375
```

方式 A 简单、一步到位，但等于把 Docker 守护进程的完整控制权交给了这个容器。
问题的核心是：**这个「完整控制权」到底有多完整？有没有办法只给「读列表」这一件事？**

---

## 二、机制：为什么挂 socket 等价于宿主机 root

这不是比喻，也不是某个项目的实现缺陷，是 Docker 的设计。

### 2.1 基本事实

```
dockerd 守护进程        以 root 身份运行
/var/run/docker.sock    由 root 创建，权限通常是 root:docker 0660
鉴权方式                能读写这个文件 = 通过鉴权。没有第二道验证
```

Docker 的客户端-服务端模型里，socket 就是唯一的信任边界。谁能连上，谁就能下命令。

### 2.2 从「能连上 socket」到「宿主机 root」的完整路径

```bash
# 在容器内执行，前提：这个容器挂载了 docker.sock

# 1. 用 Docker API 起一个新容器，把宿主机根目录挂进去
docker run -it --rm -v /:/host alpine chroot /host bash

# 2. 现在你在宿主机根目录下了，是 root
```

整个过程：

```
容器内进程  →  调用 Docker API  →  dockerd（root）照做  →  新容器持有宿主机 /
```

不需要：

```
✗ --privileged
✗ 内核漏洞 / 容器逃逸 exploit
✗ seccomp 绕过
✗ 原有的只读根文件系统、非 root 用户等加固（全部失效，因为新容器是新开的）
```

### 2.3 `:ro` 为什么不管用（最容易被误解的一条）

```yaml
- /var/run/docker.sock:/var/run/docker.sock:ro
```

`:ro` 约束的是「这个**文件**能否被写入」，而 socket 的用法是「往它发请求」。

```
:ro 阻止的     删除/覆盖这个 socket 文件本身
:ro 不阻止     通过它发送任意 Docker API 请求
```

换句话说：`:ro` 让攻击者不能把门拆掉，但门照样能推开。

### 2.4 出处

- Docker 官方安全文档：容器默认的能力集与挂载可能提供不完整的隔离
  https://docs.docker.com/engine/security
- Netdata 的专题分析：「Mounting `/var/run/docker.sock` into a container grants that container a root-equivalent privilege boundary. This is not a Docker vulnerability.」
  https://www.netdata.cloud/guides/docker/docker-socket-security
- Dockge 项目自己的 issue #849，标题就是「Docker socket exposure enables complete host compromise」
  https://github.com/louislam/dockge/issues/849
- Mironsoft 的分析文章，含三种替代方案对比
  https://www.mironsoft.de/en/blog/docker-docker-socket-mounting-risks-and-safe-alternatives

各家措辞一致，没有争议。

---

## 三、关键问题：为什么没有「只读模式」

这是整件事的关键。很多人第一反应是「我只读列表，不给写权限不就行了」。

**Docker API 不区分读和写的权限级别。**

```
想做的事                调用的接口                  同时也获得了
────────────────────────────────────────────────────────────────────
列出运行中的容器        GET  /containers/json       —
查看某个容器详情        GET  /containers/{id}/json  —
启动 / 停止容器         POST /containers/{id}/start —
创建新容器              POST /containers/create     ← 提权入口就在这
在容器里执行命令        POST /containers/{id}/exec  —
```

这些接口**走同一个 socket、同一套鉴权、同一个权限级别**。Docker 没有「只读 token」这种东西。

所以对应用开发者来说，选项只有两个：

```
① 挂 socket  →  拿到全部能力（也包括自己不想用的那些）
② 不挂 socket →  完全拿不到容器信息，功能做不了
```

「只给读」在原生 Docker 里**技术上不存在**。

### 3.1 唯一的中间档：中间放一个代理

既然 Docker 本身不做区分，那就在中间加一层做区分：

```
JustNav  →  socket-proxy  →  dockerd
           （只放行 GET）    （root）
```

`socket-proxy` 是一个只做转发的小服务，按白名单过滤请求。它自己仍然要挂 socket（否则没法转发），但它的攻击面是「一个 haproxy 式的转发器」，而不是「一个完整的 Web 应用 + 登录逻辑 + 前端」。

---

## 四、同类项目怎么做的

**结论：绝大多数直挂，没有额外隔离。**

### Homarr（同类里最主流的之一）

官方文档的做法：

```yaml
services:
  homarr:
    image: ghcr.io/homarr-labs/homarr:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./homarr/appdata:/appdata
```

文档原话是「Mount the Docker socket into the Homarr container」，并注明这是 Docker 集成的必要步骤。
- https://homarr.dev/docs/integrations/docker
- https://homarr.dev/docs/getting-started/installation/docker

### 其他

Dashy、Homer、sun-panel 等做容器发现的导航页，社区教程基本都是直挂。**没见到哪个主流项目默认配 socket-proxy。**

### 这说明了什么

两种可能的解读，都成立：

```
解读 A   大家都这么干 → 说明实际风险在内网自用场景下可以接受
解读 B   大家都这么干 → 说明这个行业普遍没把这个当回事，不代表没风险
```

客观事实是：**直挂是行业惯例，但惯例不等于零风险。**

---

## 五、风险到底多大：分层看

风险 = 发生概率 × 影响。影响是确定的（root），所以关键是概率。

```
部署场景                                    实际风险    说明
──────────────────────────────────────────────────────────────────────────────
内网自用，单人，只跑自己的容器              低          攻击者得先进入内网，进入内网后
                                                        有的是别的路子，导航页未必是首选
内网 + 反代到公网，只有自己访问             中低        取决于应用本身有没有 0day
反代到公网 + 多人使用 / 开放注册            高          等于把宿主机 root 挂在公网上
rootless Docker                             低          最坏落到非特权用户，不是 root
```

### 需要澄清的一点

挂 socket **不代表这个应用会主动干坏事**。导航页不会无缘无故创建特权容器。

真正的意思是：

```
一旦这个应用被攻破（取决于它自己的漏洞）
攻击者不需要再想办法从容器里逃逸
—— 门本来就是开的
```

所以它把一个「需要两步」的攻击（攻破应用 + 容器逃逸）变成了「一步」。

---

## 六、可选方案对比

### 方案 1：直接挂 socket（当前主流做法）

```yaml
volumes:
  - /var/run/docker.sock:/var/run/docker.sock
```

```
工作量     零
功能       完整
风险       容器被攻破 = 宿主机 root
适用       内网自用
```

### 方案 2：socket-proxy，只放行 GET

```yaml
services:
  socket-proxy:
    image: tecnativa/docker-socket-proxy:latest
    restart: unless-stopped
    environment:
      CONTAINERS: 1     # 只开 /containers/* 的查询
      POST: 0           # 关闭所有写操作
      EXEC: 0
      IMAGES: 0
      VOLUMES: 0
      NETWORKS: 0
      INFO: 0
      EVENTS: 0
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    networks: [internal]

  justnav:
    environment:
      JUSTNAV_DOCKER_HOST: tcp://socket-proxy:2375
    networks: [internal]

networks:
  internal:
    internal: true      # 这个网络不出宿主机
```

```
工作量     多一个容器（约 20MB 内存），多两行 compose
功能       完整（对只读列表这个需求来说）
风险       应用被攻破只能读容器列表，拿不到 root
           要提权还得再攻破 socket-proxy
适用       想加固但不想改代码
注意       socket-proxy 自己仍挂 socket，这是绕不过去的
           （它必须能读 socket 才能转发）
```

### 方案 3：rootless Docker

```
工作量     大。要把整个 Docker 环境改成 rootless 模式
           已有的容器、数据、权限都要迁移
功能       完整
风险       低。最坏落到非特权用户，不是 root
适用       新建环境，或者愿意做一次大迁移
```

### 方案 4：不挂 socket，手工添加

```
工作量     零
功能       失去自动发现，服务得手动一条条加
风险       无
适用       容器不多、或者不在乎自动发现
```

---

## 七、JustNav 这个项目的具体情况

### 它实际用了哪些 Docker API

全仓搜索的结果：**只有一条请求**。

```go
// internal/dockerx/docker.go:64
GET /containers/json?all=true
```

没有创建容器、没有启停、没有 exec、没有镜像或卷操作。这是刻意的——项目定位是「只做导航」，明确不做容器管理。

### 这意味着什么

```
方案 2（socket-proxy 只放行 GET）对它是【零功能损失】
不是砍功能换安全，而是本来就不需要那些功能
```

### 代码层已经预留了

```go
// internal/config/config.go
// 推荐填 socket-proxy 地址（如 tcp://docker-proxy:2375）
DockerHost string

// internal/dockerx/docker.go:160
case strings.HasPrefix(host, "tcp://"):
    return "http://" + strings.TrimPrefix(host, "tcp://"), ...
```

`api.go` 的错误提示里也直接写了「建议接 docker-socket-proxy」。**支持 tcp 地址，不需要改代码。**

---

## 八、待外部确认的问题

以下几点我自己没有把握，希望得到不同意见：

1. **socket-proxy 的实际防护效果**：如果攻击者已经攻破了 JustNav，他能对 socket-proxy 发起什么？socket-proxy 自身有没有历史 CVE？

2. **`internal: true` 网络的作用**：JustNav 和 socket-proxy 放在一个不出宿主机的 docker 网络里，是否足以防止外部直接访问 socket-proxy 的 2375 端口？有没有遗漏的暴露面？

3. **tecnativa/docker-socket-proxy 的配置细度**：`CONTAINERS: 1` 放行的是整组 `/containers/*` 的 GET，包括详情、日志、stats。对「只读列表」这个需求来说，多放行的那部分有没有实际风险？

4. **行业惯例是否可信**：Homarr 等主流项目都直挂，是因为「风险可接受」还是「大家都懒得做」？有没有哪个自托管项目默认配了 socket-proxy 的先例？

5. **内网自用的实际威胁模型**：家里 NAS 只在内网开放、不开公网反代的情况下，挂 socket 的风险是否已经低到可以忽略？还是说一旦有任何一个内网设备被钓鱼，这条链就能被利用？

6. **有没有第五种方案**：比如 Docker 的 `--userns-remap`、SELinux/AppArmor 策略、或者读取 `/proc` 之类不经过 daemon 的方式拿到容器列表？

---

## 附录：核对用的事实清单

如果对方要复核，以下都可以自己验证：

```bash
# 1. 看看你的 docker.sock 权限
ls -l /var/run/docker.sock
# 通常输出 srw-rw---- 1 root docker

# 2. 验证 :ro 挡不住 API 调用（在挂了 socket 的容器里执行）
docker run -it --rm -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v /:/host alpine chroot /host id
# 如果输出 uid=0(root)，说明 :ro 没挡住

# 3. 看某个应用实际调了哪些 Docker API
# 以 JustNav 为例
grep -rn 'MethodGet\|MethodPost\|/containers' internal/dockerx/
```

第 2 条是最直接的证明，一分钟能跑完。
