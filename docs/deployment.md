# 部署与接入

面向部署运维：两种接入方式、反代、多 bot 与配置项。判定逻辑见
[判定链路](how-it-works.md)。

## 环境要求

- 一个 Telegram bot（从 [@BotFather](https://t.me/BotFather) 申请）与你的 TG 数字 user_id
- 长轮询模式不需要公网地址，但只能做配置管理（主 bot 不入群、不判定）；
  要真正反广告需要 webhook 模式 + 一个 https 域名
- 自建：Go 1.25+；也可以直接用 `ghcr.io/x6nux/menshen` 镜像（amd64 / arm64）

## 两种接入方式

### 长轮询

配置里填 `bot_token` 就是这个模式，不需要公网地址。启动时会先
`deleteWebhook` —— getUpdates 与 webhook 互斥，库里留着上次的 webhook
会让 getUpdates 一直拿 409，而表现为「一条消息都收不到」。

⚠️ 长轮询模式下只有配置里那个**主 bot** 能收更新，而主 bot 不入群、
不判定广告 —— 所以这个模式只能做配置管理。要真正反广告，必须配
`public_url` 切到 webhook 模式，再在面板里接入工作 bot。

### Webhook（多 bot 接入）

配置里填 `public_url` 即切换到这个模式。它是回调地址的**前缀**，
可以带任意层子路径：

```yaml
public_url: "https://ad.example.com/tg"
# → 回调地址 https://ad.example.com/tg/<BOT_TOKEN>/webhook
```

已注册的每个 bot 由本进程自动 `setWebhook`，不必手工调用。

- 路径里的 token 本身就是凭证，与官方 `/bot<TOKEN>/method` 的认证模型一致
  —— 知道 token 的人本来就能直接控制那个 bot，伪造推送并不会多出任何权限
- **接入是注册制**：token 必须先在面板上登记过（绑定 owner），
  没登记的一律 401，连 `getMe` 都不会发。`getMe` 验真在登记时做一次
- 路径解析锚在 token 自身的格式上，所以 `/bot<TOKEN>`（官方形态）
  与反代再套几层子路径都认得出来
- 每个 bot 的更新由**一个** worker 串行消费，与长轮询语义完全一致
- `GET /healthz` 供反代探活

**多 bot 必须用 webhook。** 长轮询模式下接入子 bot 会被直接拒绝：getUpdates
要为每个 bot 各开一条长连接，而 Telegram 的限速同时按 bot 和按出口 IP 算 ——
几个 bot 一起轮询会互相挤掉配额，表现是所有 bot 一起变慢、一起丢更新，
且没有任何一条日志指向真正的原因。

已经接入的子 bot 在切回长轮询后不会启动（记录留着，面板上标为「已启用但
没有在运行」），改回 webhook 重启即自动恢复。

### 反代示例（nginx）

```nginx
location / {
    proxy_pass http://127.0.0.1:8081;
    proxy_set_header Host $host;
}
```

监听地址默认只听回环。这个端口上的请求等同于「以 bot 身份收发消息」，
不要直接暴露到公网。

## 配置

全部配置项见 [config.example.yaml](../config.example.yaml)，常用几项：

| 配置项 | 说明 |
|---|---|
| `admin_ids` | 主管理员 TG 数字 user_id，可多个 |
| `bot_token` | 管理面板所在的 bot；长轮询模式下唯一工作的 bot |
| `public_url` | 填了即 webhook 模式，多 bot 必填 |
| `listen_addr` | webhook 模式监听地址，默认 `127.0.0.1:8081` |
| `tg_api_base` | Bot API 基址，默认走反代 |
| `tg_proxy` / `ai_proxy` | 出网代理，TG 与 AI 分开配，可留空 |
| `db_path` | SQLite 路径，库里有上游 api_key 明文，建议 `chmod 600` |
| `turnstile_site_key` / `turnstile_secret` | 申诉网页验证的 Cloudflare Turnstile 密钥；不配则申诉降级为「联系群管理员」 |
| `client_ip_header` | 取真实客户端 IP 的请求头，如 `CF-Connecting-IP`、`X-Real-IP` |

⚠️ 用申诉网页时，**Turnstile 后台的域名白名单必须包含 `public_url` 的主机名**，
否则验证组件在页面上根本加载不出来，所有申诉都卡在网页这一步。

`client_ip_header` 的前提是 `listen_addr` 只绑回环、流量全部经过反代 ——
这个请求头才可信。原始 IP 与浏览器特征只存在数据库里，不写进任何
Telegram 消息、也不上任何网页。

**每一项都可以改用环境变量**：键名加 `MENSHEN_` 前缀转大写，多个值用逗号分隔，
例如 `MENSHEN_ADMIN_IDS="1,2,3"`、`MENSHEN_BOT_TOKEN="..."`。环境变量**压过**
配置文件，且配置文件可以完全不存在（容器里常见的做法是全用环境变量）。

加前缀是为了不跟别的东西撞车 —— 尤其 `HTTP_PROXY` 已经被 Go 自己用着了。
代理留空 = 沿用 `HTTP_PROXY` / `HTTPS_PROXY` 环境变量（不是直连）。

## 容器部署

```bash
git clone https://github.com/x6nux/menshen.git && cd menshen
cp config.example.yaml config.yaml   # 填 bot_token 与 admin_ids
docker compose up -d
```

或者把 `docker-compose.yml` 里的 `build: .` 换成发布镜像
`image: ghcr.io/x6nux/menshen:latest`（或钉一个版本号）。

容器里有三个与 `127.0.0.1` 有关的坑，镜像已经替你处理好前两个：

- `listen_addr`：容器里必须监听 `0.0.0.0:8081`，默认值只听容器自己的回环，
  端口映射过去也连不上（镜像已用环境变量预设，暴露面交给 compose 的端口绑定收敛）
- `db_path`：必须指向挂载卷内，否则写进容器可写层，重启即全部丢失
  （镜像已预设 `/app/data/data.db`，挂上卷即可）
- 要用宿主机上的代理得写 `http://host.docker.internal:7890`（Docker Desktop）
  或宿主机在 docker0 上的地址（Linux 常见为 172.17.0.1）

## 部署后

1. 私聊主 bot 发 `/start` 打开面板，按主菜单的「尚未配置完成」提示逐条补齐
2. 在面板里接入一个**工作 bot**，把它拉进群并**设为管理员**
   （删除消息 + 封禁用户权限），再为它添加该群的 `chat_id`
3. 新群先开演练模式跑几天，校准阈值后切正式

主 bot 自己不入群、不判定：被拉进群或频道会自动退出，这是设计如此。
判定链路有四个前置条件，缺任何一个都表现为「一条都没拦到」而不报错，
别忽略主菜单那段提示。其余静默失效点见 [README 的部署前必读](../README.md)。

## Mini App

管理员私聊里的菜单按钮「配置」打开 **Mini App**：`<public_url>/miniapp`。
仅 webhook 模式存在（轮询模式没有 HTTP 服务）。菜单按钮由服务在启动或接入
bot 时自动设置（`setChatMenuButton`，只挂在管理员私聊里）。

界面是 5 个 Tab（概览 / 机器人 / 群组 / 记录 / 我的）：工作台待办、群组批量
操作、记录内申诉分段、名单分段、设置智能控件与上游连通性测试都在这里
（详见 [面板、命令与通知](panel.md#mini-app)）。

全部配置项都能在 Mini App 里读写：上游、模型、机器人、群组、全局与单 bot
参数、白名单、管理员、联合封禁、判定记录与申诉单。鉴权用 Telegram WebApp
的 `initData` 验签（24 小时有效期），权限与面板一致。前端由 `docker build`
的 web 阶段构建并默认嵌入二进制（无需 build tag），部署无需手工构建前端。
